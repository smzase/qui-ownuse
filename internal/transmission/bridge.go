// Copyright (c) 2025-2026, s0oup and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package transmission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// webAPIVersion is the synthetic qBittorrent Web API version the bridge
// reports. It gates qui's capability flags to what Transmission can honor.
// Transmission labels are handled through the legacy add/remove tag APIs;
// newer qBittorrent-only feature gates remain disabled.
const webAPIVersion = "2.7.0"

const (
	sessionCacheTTL      = 5 * time.Second
	freeSpaceCacheTTL    = 10 * time.Second
	unsupportedMediaType = "Transmission does not support this operation"
)

// syncTorrentFields is the torrent-get field list used for the periodic
// maindata poll. Heavy per-torrent payloads (files, peers) are fetched only
// by their dedicated endpoints.
var syncTorrentFields = []string{
	"id", "hashString", "name", "status", "error", "errorString", "downloadDir",
	"totalSize", "sizeWhenDone", "leftUntilDone", "percentDone",
	"metadataPercentComplete", "addedDate", "doneDate", "activityDate",
	"dateCreated", "eta", "rateDownload", "rateUpload", "uploadedEver",
	"downloadedEver", "corruptEver", "uploadRatio", "peersConnected",
	"peersSendingToUs", "peersGettingFromUs", "trackerStats", "labels",
	"queuePosition", "downloadLimit", "downloadLimited", "uploadLimit",
	"uploadLimited", "seedRatioLimit", "seedRatioMode", "seedIdleLimit",
	"seedIdleMode", "secondsDownloading", "secondsSeeding", "isPrivate",
	"pieceCount", "magnetLink", "comment", "creator",
}

// detailTorrentFields extends the sync list with fields needed by the
// properties/files endpoints.
var detailTorrentFields = append(append([]string{}, syncTorrentFields...),
	"pieceSize", "files", "fileStats", "peers", "bandwidthPriority")

// bridgeSnapshot is the last state emitted through sync/maindata; it is the
// baseline for computing deltas.
type bridgeSnapshot struct {
	rid      int64
	torrents map[string]json.RawMessage
	tags     map[string]bool
	trackers map[string][]string
	// hashLookup maps lowercase hashes to the daemon's hashString spelling.
	hashLookup map[string]string
}

// Bridge is an http.RoundTripper that emulates the qBittorrent WebUI API v2
// surface on top of a Transmission RPC connection. qui's go-qbittorrent
// client talks to it as if it were a qBittorrent daemon.
type Bridge struct {
	rpc *rpcClient

	mu       sync.Mutex
	snapshot *bridgeSnapshot
	rid      int64
	peerRid  int64

	sessionMu      sync.Mutex
	sessionCache   *session
	sessionFetched time.Time
	freeSpaceMu    sync.Mutex
	freeSpacePath  string
	freeSpaceBytes int64
	freeSpaceAt    time.Time
}

// NewBridge creates a Transmission bridge for the given daemon host. The
// username/password are the daemon's RPC credentials (basic auth).
func NewBridge(host, username, password string, tlsSkipVerify bool, timeout time.Duration) (*Bridge, error) {
	rpc, err := newRPIClient(host, username, password, tlsSkipVerify, timeout)
	if err != nil {
		return nil, err
	}

	return &Bridge{rpc: rpc}, nil
}

// RoundTrip implements http.RoundTripper and dispatches /api/v2/* requests
// to their Transmission translations.
func (b *Bridge) RoundTrip(req *http.Request) (*http.Response, error) {
	endpoint, ok := apiEndpoint(req.URL.Path)
	if !ok {
		return errorResponse(req, http.StatusNotFound, "not a qBittorrent API path")
	}

	ctx := req.Context()

	if req.Method == http.MethodPost {
		// Normalize the body so handlers can read form values uniformly;
		// multipart (torrent uploads) is handled where needed.
		if err := req.ParseForm(); err != nil {
			return errorResponse(req, http.StatusBadRequest, "invalid form body")
		}
	}

	switch endpoint {
	case "auth/login":
		return b.handleLogin(ctx, req)

	// app
	case "app/version":
		return b.handleAppVersion(ctx, req)
	case "app/webapiVersion":
		return textResponse(req, webAPIVersion)
	case "app/buildInfo":
		return jsonResponse(req, http.StatusOK, map[string]any{
			"qt": "", "libtorrent": "", "boost": "", "openssl": "", "zlib": "",
			"bitness": 64, "platform": "transmission",
		})
	case "app/preferences":
		return b.handlePreferences(ctx, req)
	case "app/setPreferences":
		return b.handleSetPreferences(ctx, req)
	case "app/defaultSavePath":
		sess, err := b.getSession(ctx)
		if err != nil {
			return rpcErrorResponse(req, err)
		}
		return textResponse(req, sess.DownloadDir)
	case "app/getDirectoryContent":
		return jsonResponse(req, http.StatusOK, []any{})

	// sync
	case "sync/maindata":
		return b.handleMaindata(ctx, req)
	case "sync/torrentPeers":
		return b.handleTorrentPeers(ctx, req)

	// transfer
	case "transfer/info":
		return b.handleTransferInfo(ctx, req)
	case "transfer/speedLimitsMode":
		return b.handleSpeedLimitsMode(ctx, req)
	case "transfer/toggleSpeedLimitsMode":
		return b.handleToggleSpeedLimits(ctx, req)
	case "transfer/banPeers":
		return errorResponse(req, http.StatusMethodNotAllowed, "Transmission does not support banning peers")

	// torrents - reads
	case "torrents/info":
		return b.handleTorrentsInfo(ctx, req)
	case "torrents/properties":
		return b.handleTorrentProperties(ctx, req)
	case "torrents/trackers":
		return b.handleTorrentTrackers(ctx, req)
	case "torrents/webseeds":
		return jsonResponse(req, http.StatusOK, []any{})
	case "torrents/pieceStates":
		return jsonResponse(req, http.StatusOK, []int{})
	case "torrents/files":
		return b.handleTorrentFiles(ctx, req)
	case "torrents/categories":
		return b.handleCategories(ctx, req)
	case "torrents/tags":
		return b.handleTags(ctx, req)

	// torrents - actions
	case "torrents/add":
		return b.handleAddTorrent(ctx, req)
	case "torrents/delete":
		return b.handleDelete(ctx, req)
	case "torrents/pause", "torrents/stop":
		return b.simpleTorrentAction(ctx, req, "torrent-stop", nil)
	case "torrents/resume", "torrents/start":
		return b.simpleTorrentAction(ctx, req, "torrent-start", nil)
	case "torrents/recheck":
		return b.simpleTorrentAction(ctx, req, "torrent-verify", nil)
	case "torrents/reannounce":
		return b.simpleTorrentAction(ctx, req, "torrent-reannounce", nil)
	case "torrents/setLocation":
		return b.handleSetLocation(ctx, req)
	case "torrents/addTags":
		return b.handleModifyTags(ctx, req, tagOperationAdd)
	case "torrents/removeTags":
		return b.handleModifyTags(ctx, req, tagOperationRemove)
	case "torrents/setTags":
		return b.handleModifyTags(ctx, req, tagOperationSet)
	case "torrents/increasePrio":
		return b.simpleTorrentAction(ctx, req, "queue-move-up", nil)
	case "torrents/decreasePrio":
		return b.simpleTorrentAction(ctx, req, "queue-move-down", nil)
	case "torrents/topPrio":
		return b.simpleTorrentAction(ctx, req, "queue-move-top", nil)
	case "torrents/bottomPrio":
		return b.simpleTorrentAction(ctx, req, "queue-move-bottom", nil)
	case "torrents/setShareLimits":
		return b.handleSetShareLimits(ctx, req)
	case "torrents/setUploadLimit":
		return b.handleSetLimit(ctx, req, "uploadLimit", "uploadLimited")
	case "torrents/setDownloadLimit":
		return b.handleSetLimit(ctx, req, "downloadLimit", "downloadLimited")
	case "torrents/filePrio":
		return b.handleFilePrio(ctx, req)
	case "torrents/rename":
		return b.handleRename(ctx, req, renameTorrent)
	case "torrents/renameFile":
		return b.handleRename(ctx, req, renamePath)
	case "torrents/renameFolder":
		return b.handleRename(ctx, req, renamePath)
	case "torrents/editTracker":
		return b.handleEditTracker(ctx, req)
	case "torrents/addTrackers":
		return b.handleAddTrackers(ctx, req)
	case "torrents/removeTrackers":
		return b.handleRemoveTrackers(ctx, req)
	case "torrents/export":
		return errorResponse(req, http.StatusMethodNotAllowed, "Transmission does not support torrent export")

	// unsupported feature families
	case "torrents/toggleSequentialDownload", "torrents/toggleFirstLastPiecePrio",
		"torrents/setSuperSeeding", "torrents/setForceStart", "torrents/setAutoManagement",
		"torrents/setCategory", "torrents/createCategory", "torrents/editCategory", "torrents/removeCategories",
		"torrents/createTags", "torrents/deleteTags", "torrents/setComment",
		"torrents/addPeers",
		"rss/items", "rss/addFolder", "rss/addFeed", "rss/setFeedURL",
		"rss/removeItem", "rss/rules", "rss/setRule", "rss/renameRule",
		"rss/removeRule", "rss/matchingArticles":
		return errorResponse(req, http.StatusMethodNotAllowed, unsupportedMediaType)

	default:
		return errorResponse(req, http.StatusNotFound, fmt.Sprintf("unsupported endpoint %q for Transmission", endpoint))
	}
}

// apiEndpoint extracts the endpoint after the /api/v2/ prefix.
func apiEndpoint(requestPath string) (string, bool) {
	idx := strings.Index(requestPath, "/api/v2/")
	if idx < 0 {
		return "", false
	}
	endpoint := strings.Trim(requestPath[idx+len("/api/v2/"):], "/")
	if endpoint == "" {
		return "", false
	}
	return endpoint, true
}

// --- auth -----------------------------------------------------------------

func (b *Bridge) handleLogin(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Validate the credentials against the daemon: session-get is the
	// cheapest authenticated call. The RPC client uses the same configured
	// pair the login form carried, so this doubles as credential validation.
	var sess session
	if err := b.rpc.call(ctx, "session-get", nil, &sess); err != nil {
		if err == ErrUnauthorized { //nolint:errorlint // sentinel comparison
			return textResponse(req, "Fails.")
		}
		return rpcErrorResponse(req, err)
	}

	resp, err := textResponse(req, "Ok.")
	if err != nil {
		return nil, err
	}
	resp.Header.Add("Set-Cookie", "SID=transmission-bridge; Path=/; Max-Age=604800")
	return resp, nil
}

// --- app ------------------------------------------------------------------

func (b *Bridge) handleAppVersion(ctx context.Context, req *http.Request) (*http.Response, error) {
	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	return textResponse(req, sess.Version)
}

func (b *Bridge) handlePreferences(ctx context.Context, req *http.Request) (*http.Response, error) {
	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	prefs := map[string]any{
		"save_path":            sess.DownloadDir,
		"temp_path":            sess.IncompleteDir,
		"temp_path_enabled":    sess.IncompleteDirEnabled,
		"up_limit":             limitBytes(sess.SpeedLimitUpEnabled, sess.SpeedLimitUp),
		"dl_limit":             limitBytes(sess.SpeedLimitDownEnabled, sess.SpeedLimitDown),
		"max_ratio_enabled":    sess.SeedRatioLimited,
		"max_ratio":            sess.SeedRatioLimit,
		"alt_speed_enabled":    sess.AltSpeedEnabled,
		"alt_up_limit":         sess.AltSpeedUp * 1024,
		"alt_dl_limit":         sess.AltSpeedDown * 1024,
		"queueing_enabled":     false,
		"dht":                  sess.DHTEnabled,
		"pex":                  sess.PexEnabled,
		"start_paused_enabled": false,
		"auto_tmm_enabled":     false,
		"use_subcategories":    false,
		"locale":               "en",
	}

	return jsonResponse(req, http.StatusOK, prefs)
}

func (b *Bridge) handleSetPreferences(ctx context.Context, req *http.Request) (*http.Response, error) {
	raw := req.FormValue("json")
	var prefs map[string]any
	if err := json.Unmarshal([]byte(raw), &prefs); err != nil {
		return errorResponse(req, http.StatusBadRequest, "invalid preferences payload")
	}

	args := make(map[string]any)
	if v, ok := prefs["save_path"].(string); ok && v != "" {
		args["download-dir"] = v
	}
	if v, ok := prefs["up_limit"].(float64); ok {
		if v > 0 {
			args["speed-limit-up"] = int64(v / 1024)
			args["speed-limit-up-enabled"] = true
		} else {
			args["speed-limit-up-enabled"] = false
		}
	}
	if v, ok := prefs["dl_limit"].(float64); ok {
		if v > 0 {
			args["speed-limit-down"] = int64(v / 1024)
			args["speed-limit-down-enabled"] = true
		} else {
			args["speed-limit-down-enabled"] = false
		}
	}
	if v, ok := prefs["alt_speed_enabled"].(bool); ok {
		args["alt-speed-enabled"] = v
	}

	if len(args) > 0 {
		if err := b.rpc.call(ctx, "session-set", args, nil); err != nil {
			return rpcErrorResponse(req, err)
		}
		b.invalidateSessionCache()
	}

	return textResponse(req, "Ok.")
}

// --- sync -----------------------------------------------------------------

func (b *Bridge) handleMaindata(ctx context.Context, req *http.Request) (*http.Response, error) {
	requestedRid, _ := strconv.ParseInt(req.URL.Query().Get("rid"), 10, 64)

	torrents, err := b.fetchTorrents(ctx, syncTorrentFields)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	stats, err := b.fetchStats(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	freeSpace := b.fetchFreeSpace(ctx, sess.DownloadDir)

	// Assemble the new snapshot.
	newTorrents := make(map[string]json.RawMessage, len(torrents))
	tags := make(map[string]bool)
	trackers := make(map[string][]string)
	hashLookup := make(map[string]string, len(torrents))
	var dlSpeed, upSpeed int64

	for i := range torrents {
		t := &torrents[i]
		qt := qbitTorrentFrom(t)
		data, err := json.Marshal(qt)
		if err != nil {
			continue
		}
		hash := qt.Hash
		newTorrents[hash] = data
		hashLookup[hash] = t.HashString

		for _, tag := range strings.Split(qt.Tags, ", ") {
			if tag != "" {
				tags[tag] = true
			}
		}
		for _, stat := range t.TrackerStats {
			if domain := trackerDomain(stat.Announce); domain != "" {
				trackers[domain] = append(trackers[domain], hash)
			}
		}
		dlSpeed += qt.DlSpeed
		upSpeed += qt.UpSpeed
	}

	b.mu.Lock()
	b.rid++
	newRid := b.rid
	prev := b.snapshot
	fullUpdate := prev == nil || requestedRid == 0 || requestedRid != prev.rid

	snapshot := &bridgeSnapshot{
		rid:        newRid,
		torrents:   newTorrents,
		tags:       tags,
		trackers:   trackers,
		hashLookup: hashLookup,
	}
	b.snapshot = snapshot
	b.mu.Unlock()

	serverState := b.buildServerState(sess, stats, freeSpace, dlSpeed, upSpeed)

	payload := make(map[string]any)
	payload["rid"] = newRid
	payload["server_state"] = serverState

	if fullUpdate {
		payload["full_update"] = true
		payload["torrents"] = newTorrents
		payload["categories"] = map[string]map[string]string{}
		payload["tags"] = sortedKeys(tags)
		payload["trackers"] = trackers
	} else {
		addMaindataDelta(payload, prev, snapshot)
	}

	return jsonResponse(req, http.StatusOK, payload)
}

func addMaindataDelta(payload map[string]any, prev, current *bridgeSnapshot) {
	payload["full_update"] = false

	changed := make(map[string]json.RawMessage)
	for hash, data := range current.torrents {
		if old, ok := prev.torrents[hash]; !ok || !jsonEqual(old, data) {
			changed[hash] = data
		}
	}
	if len(changed) > 0 {
		payload["torrents"] = changed
	}

	removed := make([]string, 0)
	for hash := range prev.torrents {
		if _, ok := current.torrents[hash]; !ok {
			removed = append(removed, hash)
		}
	}
	if len(removed) > 0 {
		payload["torrents_removed"] = removed
	}

	if !mapsEqual(prev.tags, current.tags) {
		payload["tags"] = sortedKeys(current.tags)
		if removedTags := mapKeysMissing(current.tags, prev.tags); len(removedTags) > 0 {
			payload["tags_removed"] = removedTags
		}
	}
	if !trackersEqual(prev.trackers, current.trackers) {
		payload["trackers"] = current.trackers
	}
}

func (b *Bridge) buildServerState(sess *session, stats *sessionStats, freeSpace int64, dlSpeed, upSpeed int64) map[string]any {
	globalRatio := 0.0
	if stats.CumulativeStats.DownloadedBytes > 0 {
		globalRatio = float64(stats.CumulativeStats.UploadedBytes) / float64(stats.CumulativeStats.DownloadedBytes)
	}

	return map[string]any{
		"alltime_dl":             stats.CumulativeStats.DownloadedBytes,
		"alltime_ul":             stats.CumulativeStats.UploadedBytes,
		"connection_status":      "connected",
		"dht_nodes":              0,
		"dl_info_data":           stats.DownloadedBytes,
		"dl_info_speed":          dlSpeed,
		"dl_rate_limit":          limitBytes(sess.SpeedLimitDownEnabled, sess.SpeedLimitDown),
		"free_space_on_disk":     freeSpace,
		"global_ratio":           fmt.Sprintf("%.2f", globalRatio),
		"queueing":               false,
		"refresh_interval":       2000,
		"total_peer_connections": 0,
		"up_info_data":           stats.UploadedBytes,
		"up_info_speed":          upSpeed,
		"up_rate_limit":          transmissionUploadRateLimit(sess),
		"use_alt_speed_limits":   sess.AltSpeedEnabled,
		"use_subcategories":      false,
	}
}

func transmissionUploadRateLimit(sess *session) int64 {
	if sess.AltSpeedEnabled {
		return limitBytes(true, sess.AltSpeedUp)
	}
	return limitBytes(sess.SpeedLimitUpEnabled, sess.SpeedLimitUp)
}

func (b *Bridge) handleTorrentPeers(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.URL.Query().Get("hash")

	torrents, err := b.fetchTorrents(ctx, []string{"id", "hashString", "peers"})
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	peers := make(map[string]any)
	for i := range torrents {
		if normalizeHash(torrents[i].HashString) != normalizeHash(hash) {
			continue
		}
		for _, p := range torrents[i].Peers {
			flags := ""
			if p.IsUploadingTo {
				flags += "U"
			}
			if p.IsDownloadingFrom {
				flags += "D"
			}
			if p.IsUTP {
				flags += "P"
			}
			peers[p.Address] = map[string]any{
				"ip":         p.Address,
				"client":     p.ClientName,
				"progress":   p.Progress,
				"dl_speed":   p.RateToClient,
				"up_speed":   p.RateToPeer,
				"flags":      flags,
				"connection": map[bool]string{true: "uTP", false: "TCP"}[p.IsUTP],
			}
		}
		break
	}

	b.mu.Lock()
	b.peerRid++
	peerRid := b.peerRid
	b.mu.Unlock()

	return jsonResponse(req, http.StatusOK, map[string]any{
		"rid":         peerRid,
		"full_update": true,
		"peers":       peers,
		"show_flags":  false,
	})
}

// --- torrents: reads --------------------------------------------------------

func (b *Bridge) handleTorrentsInfo(ctx context.Context, req *http.Request) (*http.Response, error) {
	torrents, err := b.fetchTorrents(ctx, syncTorrentFields)
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	query := req.URL.Query()
	hashFilter := make(map[string]bool)
	for _, hash := range splitPipe(query.Get("hashes")) {
		hashFilter[normalizeHash(hash)] = true
	}
	filter := query.Get("filter")
	category := query.Get("category")
	tag := query.Get("tag")

	results := make([]qbitTorrent, 0, len(torrents))
	for i := range torrents {
		qt := qbitTorrentFrom(&torrents[i])
		if len(hashFilter) > 0 && !hashFilter[normalizeHash(qt.Hash)] {
			continue
		}
		if category != "" && qt.Category != category {
			continue
		}
		if tag != "" && !hasTag(qt.Tags, tag) {
			continue
		}
		if filter != "" && filter != "all" && !matchesFilter(&qt, filter) {
			continue
		}
		results = append(results, qt)
	}

	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	if offset > 0 && offset < len(results) {
		results = results[offset:]
	} else if offset > 0 {
		results = nil
	}
	if limit > 0 && limit < len(results) {
		results = results[:limit]
	}

	return jsonResponse(req, http.StatusOK, results)
}

// matchesFilter applies qBittorrent's server-side torrent filter semantics
// on the mapped state.
func matchesFilter(t *qbitTorrent, filter string) bool {
	state := t.State
	switch filter {
	case "active":
		return t.DlSpeed > 0 || t.UpSpeed > 0
	case "inactive":
		return t.DlSpeed == 0 && t.UpSpeed == 0
	case "completed":
		return t.Progress >= 1
	case "paused", "stopped":
		return state == "stoppedDL" || state == "stoppedUP" || state == "pausedDL" || state == "pausedUP"
	case "resumed", "running":
		return state != "stoppedDL" && state != "stoppedUP" && state != "pausedDL" && state != "pausedUP"
	case "downloading":
		return strings.Contains(state, "DL") || state == "metaDL"
	case "seeding", "uploading":
		return strings.Contains(state, "UP")
	case "stalled":
		return state == "stalledDL" || state == "stalledUP"
	case "stalled_downloading":
		return state == "stalledDL"
	case "stalled_uploading":
		return state == "stalledUP"
	case "checking":
		return strings.HasPrefix(state, "checking")
	case "errored":
		return state == "error" || state == "missingFiles"
	case "moving":
		return false
	default:
		return true
	}
}

func (b *Bridge) handleTorrentProperties(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.URL.Query().Get("hash")

	t, err := b.fetchSingleTorrent(ctx, hash, detailTorrentFields)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if t == nil {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	qt := qbitTorrentFrom(t)
	timeActive := qt.TimeActive
	// qBittorrent exposes average speeds as integers; fractional values make
	// go-qbittorrent reject the entire properties response.
	dlAvg, upAvg := int64(0), int64(0)
	if timeActive > 0 {
		dlAvg = t.DownloadedEver / timeActive
		upAvg = t.UploadedEver / timeActive
	}

	props := map[string]any{
		"addition_date":            t.AddedDate,
		"comment":                  t.Comment,
		"completion_date":          t.DoneDate,
		"created_by":               t.Creator,
		"creation_date":            t.DateCreated,
		"dl_limit":                 qt.DlLimit,
		"dl_speed":                 t.RateDownload,
		"dl_speed_avg":             dlAvg,
		"download_path":            "",
		"eta":                      mapEta(t.Eta),
		"hash":                     qt.Hash,
		"infohash_v1":              qt.Hash,
		"infohash_v2":              "",
		"is_private":               t.IsPrivate,
		"last_seen":                t.ActivityDate,
		"name":                     t.Name,
		"nb_connections":           0,
		"nb_connections_limit":     0,
		"peers":                    t.PeersConnected,
		"peers_total":              t.PeersConnected,
		"piece_size":               t.PieceSize,
		"pieces_have":              int64(float64(t.PieceCount) * t.PercentDone),
		"pieces_num":               t.PieceCount,
		"reannounce":               0,
		"save_path":                t.DownloadDir,
		"seeding_time":             t.SecondsSeeding,
		"seeds":                    t.PeersSendingToUs,
		"seeds_total":              qt.NumComplete,
		"share_ratio":              t.Ratio,
		"time_elapsed":             timeActive,
		"total_downloaded":         t.DownloadedEver,
		"total_downloaded_session": t.DownloadedEver,
		"total_size":               t.SizeWhenDone,
		"total_uploaded":           t.UploadedEver,
		"total_uploaded_session":   t.UploadedEver,
		"total_wasted":             t.CorruptEver,
		"up_limit":                 qt.UpLimit,
		"up_speed":                 t.RateUpload,
		"up_speed_avg":             upAvg,
	}

	return jsonResponse(req, http.StatusOK, props)
}

func (b *Bridge) handleTorrentTrackers(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.URL.Query().Get("hash")

	t, err := b.fetchSingleTorrent(ctx, hash, []string{"id", "hashString", "trackerStats"})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if t == nil {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	trackers := make([]map[string]any, 0, len(t.TrackerStats)+3)
	for i := range t.TrackerStats {
		stat := &t.TrackerStats[i]
		status := 1 // not contacted yet
		if stat.AnnounceState == 0 {
			status = 0 // disabled
		} else if stat.LastAnnounceSucceeded {
			status = 2 // working
		} else if stat.LastAnnounceTime > 0 {
			status = 4 // not working
		}
		trackers = append(trackers, map[string]any{
			"url":            stat.Announce,
			"status":         status,
			"num_peers":      0,
			"num_seeds":      stat.SeederCount,
			"num_leeches":    stat.LeecherCount,
			"num_downloaded": stat.DownloadCount,
			"msg":            stat.LastAnnounceResult,
		})
	}

	pseudo := func(name string, enabled bool) map[string]any {
		status := 0
		if enabled {
			status = 2
		}
		return map[string]any{
			"url":       name,
			"status":    status,
			"num_peers": 0, "num_seeds": 0, "num_leeches": 0, "num_downloaded": 0,
			"msg": "",
		}
	}
	trackers = append(trackers, pseudo("** [DHT] **", sess.DHTEnabled))
	trackers = append(trackers, pseudo("** [PeX] **", sess.PexEnabled))
	trackers = append(trackers, pseudo("** [LSD] **", true))

	return jsonResponse(req, http.StatusOK, trackers)
}

func (b *Bridge) handleTorrentFiles(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.URL.Query().Get("hash")

	t, err := b.fetchSingleTorrent(ctx, hash, []string{"id", "hashString", "files", "fileStats", "metadataPercentComplete", "sizeWhenDone"})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if t == nil {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}
	if t.MetadataPercentDone < 1 {
		return errorResponse(req, http.StatusConflict, "torrent metadata hasn't downloaded yet")
	}

	files := make([]map[string]any, 0, len(t.Files))
	for i, f := range t.Files {
		priority := 1
		progress := 0.0
		if i < len(t.FileStats) {
			if !t.FileStats[i].Wanted {
				priority = 0
			}
			if f.Length > 0 {
				progress = float64(t.FileStats[i].BytesCompleted) / float64(f.Length)
			}
		}
		files = append(files, map[string]any{
			"availability": -1,
			"index":        i,
			"is_seed":      progress >= 1,
			"name":         f.Name,
			"piece_range":  []int{0, 0},
			"priority":     priority,
			"progress":     progress,
			"size":         f.Length,
		})
	}

	return jsonResponse(req, http.StatusOK, files)
}

func (b *Bridge) handleCategories(ctx context.Context, req *http.Request) (*http.Response, error) {
	return jsonResponse(req, http.StatusOK, map[string]map[string]string{})
}

func (b *Bridge) handleTags(ctx context.Context, req *http.Request) (*http.Response, error) {
	torrents, err := b.fetchTorrents(ctx, []string{"id", "hashString", "labels"})
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	tags := make(map[string]bool)
	for i := range torrents {
		for _, label := range normalizeLabels(torrents[i].Labels) {
			tags[label] = true
		}
	}

	return jsonResponse(req, http.StatusOK, sortedKeys(tags))
}

// --- torrents: actions ------------------------------------------------------

func (b *Bridge) handleAddTorrent(ctx context.Context, req *http.Request) (*http.Response, error) {
	contentType := req.Header.Get("Content-Type")

	added := 0
	failed := 0
	var addedIDs []string

	addOptions, err := parseAddOptions(req)
	if err != nil {
		return errorResponse(req, http.StatusBadRequest, err.Error())
	}

	if strings.HasPrefix(contentType, "multipart/form-data") {
		// The bridge only ever receives requests from the in-process
		// go-qbittorrent client, never from an external network client, so
		// the memory bound matches qui's own upload handler (256 MiB).
		if err := req.ParseMultipartForm(256 << 20); err != nil { //nolint:gosec // in-process transport, matches qui's upload limit
			return errorResponse(req, http.StatusBadRequest, "invalid multipart body")
		}
		for _, part := range req.MultipartForm.File["torrents"] {
			file, err := part.Open()
			if err != nil {
				failed++
				continue
			}
			content, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				failed++
				continue
			}
			info, err := b.addSingleTorrent(ctx, content, "", addOptions)
			if err != nil {
				failed++
				continue
			}
			added++
			if info != nil {
				addedIDs = append(addedIDs, strings.ToLower(info.HashString))
			}
		}
	} else {
		for _, rawURL := range splitURLList(req.FormValue("urls")) {
			if _, err := b.addSingleTorrent(ctx, nil, rawURL, addOptions); err != nil {
				failed++
				continue
			}
			added++
		}
	}

	return jsonResponse(req, http.StatusOK, map[string]any{
		"success_count":     added,
		"failure_count":     failed,
		"added_torrent_ids": addedIDs,
	})
}

// addOptions carries the torrent-add arguments derived from qBittorrent's
// form fields.
type addOptions struct {
	downloadDir   string
	paused        bool
	labels        []string
	uploadLimit   int64
	downloadLimit int64
	ratioLimit    float64
	hasRatioLimit bool
}

func parseAddOptions(req *http.Request) (*addOptions, error) {
	opts := &addOptions{}

	if v := req.FormValue("savepath"); v != "" {
		opts.downloadDir = v
	}
	opts.paused = req.FormValue("paused") == "true" || req.FormValue("stopped") == "true"
	if v := req.FormValue("tags"); v != "" {
		opts.labels = normalizeLabels(splitComma(v))
	}
	if v, err := strconv.ParseInt(req.FormValue("upLimit"), 10, 64); err == nil && v > 0 {
		opts.uploadLimit = v / 1024
	}
	if v, err := strconv.ParseInt(req.FormValue("dlLimit"), 10, 64); err == nil && v > 0 {
		opts.downloadLimit = v / 1024
	}
	if v, err := strconv.ParseFloat(req.FormValue("ratioLimit"), 64); err == nil && v > 0 {
		opts.ratioLimit = v
		opts.hasRatioLimit = true
	}

	return opts, nil
}

// addSingleTorrent adds one torrent by metainfo bytes or by URL and returns
// the daemon's torrent-added info (nil for duplicates, which still succeed).
func (b *Bridge) addSingleTorrent(ctx context.Context, metainfo []byte, torrentURL string, opts *addOptions) (*torrentAddedInfo, error) {
	args := make(map[string]any)
	if metainfo != nil {
		args["metainfo"] = base64.StdEncoding.EncodeToString(metainfo)
	} else {
		args["filename"] = torrentURL
	}
	if opts.downloadDir != "" {
		args["download-dir"] = opts.downloadDir
	}
	if opts.paused {
		args["paused"] = true
	}
	if len(opts.labels) > 0 {
		args["labels"] = opts.labels
	}
	if opts.uploadLimit > 0 {
		args["uploadLimit"] = opts.uploadLimit
		args["uploadLimited"] = true
	}
	if opts.downloadLimit > 0 {
		args["downloadLimit"] = opts.downloadLimit
		args["downloadLimited"] = true
	}
	if opts.hasRatioLimit {
		args["seedRatioLimit"] = opts.ratioLimit
		args["seedRatioMode"] = 1
	}

	var result torrentAddedArguments
	if err := b.rpc.call(ctx, "torrent-add", args, &result); err != nil {
		return nil, err
	}

	if result.TorrentAdded != nil {
		return result.TorrentAdded, nil
	}
	return result.TorrentDuplicate, nil
}

func (b *Bridge) handleDelete(ctx context.Context, req *http.Request) (*http.Response, error) {
	hashes := splitPipe(req.FormValue("hashes"))
	deleteFiles := req.FormValue("deleteFiles") == "true"

	ids, err := b.resolveIDs(ctx, hashes)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return textResponse(req, "Ok.")
	}

	args := map[string]any{"ids": ids}
	if deleteFiles {
		args["delete-local-data"] = true
	}

	if err := b.rpc.call(ctx, "torrent-remove", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

// simpleTorrentAction runs one RPC method over the hashes in the form.
func (b *Bridge) simpleTorrentAction(ctx context.Context, req *http.Request, method string, extra map[string]any) (*http.Response, error) {
	hashes := splitPipe(req.FormValue("hashes"))

	ids, err := b.resolveIDs(ctx, hashes)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return textResponse(req, "Ok.")
	}

	args := map[string]any{"ids": ids}
	for k, v := range extra {
		args[k] = v
	}

	if err := b.rpc.call(ctx, method, args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

func (b *Bridge) handleSetLocation(ctx context.Context, req *http.Request) (*http.Response, error) {
	hashes := splitPipe(req.FormValue("hashes"))
	location := req.FormValue("location")

	ids, err := b.resolveIDs(ctx, hashes)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return textResponse(req, "Ok.")
	}

	args := map[string]any{
		"ids":      ids,
		"location": location,
		"move":     true,
	}
	if err := b.rpc.call(ctx, "torrent-set-location", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

type tagOperation int

const (
	tagOperationAdd tagOperation = iota
	tagOperationRemove
	tagOperationSet
)

func (b *Bridge) handleModifyTags(ctx context.Context, req *http.Request, operation tagOperation) (*http.Response, error) {
	hashes := splitPipe(req.FormValue("hashes"))
	if len(hashes) == 0 {
		return textResponse(req, "Ok.")
	}

	requestedTags := normalizeLabels(splitComma(req.FormValue("tags")))
	torrents, err := b.fetchTorrents(ctx, []string{"id", "hashString", "labels"})
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	selected := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		selected[normalizeHash(hash)] = struct{}{}
	}

	for i := range torrents {
		torrent := &torrents[i]
		if _, ok := selected[normalizeHash(torrent.HashString)]; !ok {
			continue
		}

		labels := updateLabels(torrent.Labels, requestedTags, operation)
		args := map[string]any{
			"ids":    []string{torrent.HashString},
			"labels": labels,
		}
		if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
			return rpcErrorResponse(req, err)
		}
	}

	return textResponse(req, "Ok.")
}

func updateLabels(current, requested []string, operation tagOperation) []string {
	if operation == tagOperationSet {
		return normalizeLabels(requested)
	}

	labels := normalizeLabels(current)
	requestedSet := make(map[string]struct{}, len(requested))
	for _, label := range requested {
		requestedSet[label] = struct{}{}
	}

	if operation == tagOperationRemove {
		kept := labels[:0]
		for _, label := range labels {
			if _, remove := requestedSet[label]; !remove {
				kept = append(kept, label)
			}
		}
		return kept
	}

	seen := make(map[string]struct{}, len(labels)+len(requested))
	for _, label := range labels {
		seen[label] = struct{}{}
	}
	for _, label := range requested {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	return labels
}

func (b *Bridge) handleSetShareLimits(ctx context.Context, req *http.Request) (*http.Response, error) {
	hashes := splitPipe(req.FormValue("hashes"))

	ratioLimit, _ := strconv.ParseFloat(req.FormValue("ratioLimit"), 64)
	inactiveLimit, _ := strconv.ParseInt(req.FormValue("inactiveSeedingTimeLimit"), 10, 64)

	ids, err := b.resolveIDs(ctx, hashes)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return textResponse(req, "Ok.")
	}

	args := map[string]any{"ids": ids}

	// qBittorrent ratio conventions: -2 = global, -1 = unlimited, >=0 = limit.
	switch {
	case ratioLimit >= 0:
		args["seedRatioLimit"] = ratioLimit
		args["seedRatioMode"] = 1
	case ratioLimit == -1:
		args["seedRatioMode"] = 2
	default:
		args["seedRatioMode"] = 0
	}

	// Transmission has no active seeding-time limit; the idle limit is the
	// closest translation. qBittorrent uses the same -2/-1 conventions.
	switch {
	case inactiveLimit >= 0:
		args["seedIdleLimit"] = inactiveLimit
		args["seedIdleMode"] = 1
	case inactiveLimit == -1:
		args["seedIdleMode"] = 2
	default:
		args["seedIdleMode"] = 0
	}

	if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

// handleSetLimit maps setUploadLimit/setDownloadLimit. limit arrives in
// bytes/s (qBittorrent convention); Transmission wants KB/s.
func (b *Bridge) handleSetLimit(ctx context.Context, req *http.Request, limitKey, limitedKey string) (*http.Response, error) {
	hashes := splitPipe(req.FormValue("hashes"))
	limitValue, err := strconv.ParseInt(req.FormValue("limit"), 10, 64)
	if err != nil {
		return errorResponse(req, http.StatusBadRequest, "invalid limit")
	}

	ids, err := b.resolveIDs(ctx, hashes)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return textResponse(req, "Ok.")
	}

	args := map[string]any{"ids": ids}
	if limitValue > 0 {
		args[limitKey] = limitValue / 1024
		args[limitedKey] = true
	} else {
		args[limitedKey] = false
	}

	if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

func (b *Bridge) handleFilePrio(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.FormValue("hash")
	priority, err := strconv.Atoi(req.FormValue("priority"))
	if err != nil {
		return errorResponse(req, http.StatusBadRequest, "invalid priority")
	}

	var indices []int64
	for _, idStr := range strings.Split(req.FormValue("id"), "|") {
		if idStr == "" {
			continue
		}
		if idx, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			indices = append(indices, idx)
		}
	}

	ids, err := b.resolveIDs(ctx, []string{hash})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	args := map[string]any{"ids": ids}
	if priority == 0 {
		args["files-unwanted"] = indices
	} else {
		args["files-wanted"] = indices
	}

	if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

type renameKind int

const (
	renameTorrent renameKind = iota
	renamePath
)

func (b *Bridge) handleRename(ctx context.Context, req *http.Request, kind renameKind) (*http.Response, error) {
	hash := req.FormValue("hash")

	ids, err := b.resolveIDs(ctx, []string{hash})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	var oldPath, newName string
	if kind == renameTorrent {
		newName = req.FormValue("name")
		t, err := b.fetchSingleTorrent(ctx, hash, []string{"id", "hashString", "name"})
		if err != nil {
			return rpcErrorResponse(req, err)
		}
		if t == nil {
			return errorResponse(req, http.StatusNotFound, "torrent hash not found")
		}
		oldPath = t.Name
	} else {
		oldPath = req.FormValue("oldPath")
		newName = path.Base(strings.ReplaceAll(req.FormValue("newPath"), "\\", "/"))
	}

	if newName == "" {
		return errorResponse(req, http.StatusBadRequest, "new name is empty")
	}

	args := map[string]any{
		"ids":  ids,
		"path": oldPath,
		"name": newName,
	}

	if err := b.rpc.call(ctx, "torrent-rename-path", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return jsonResponse(req, http.StatusOK, map[string]string{"message": "Ok."})
}

func (b *Bridge) handleEditTracker(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.FormValue("hash")
	oldURL := req.FormValue("origUrl")
	if oldURL == "" {
		oldURL = req.FormValue("url")
	}
	newURL := req.FormValue("newUrl")

	ids, err := b.resolveIDs(ctx, []string{hash})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	args := map[string]any{
		"ids":            ids,
		"trackerReplace": [][]string{{oldURL, newURL}},
	}

	if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

func (b *Bridge) handleAddTrackers(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.FormValue("hash")
	urls := splitURLList(req.FormValue("urls"))

	ids, err := b.resolveIDs(ctx, []string{hash})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	args := map[string]any{"ids": ids, "trackerAdd": urls}
	if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

func (b *Bridge) handleRemoveTrackers(ctx context.Context, req *http.Request) (*http.Response, error) {
	hash := req.FormValue("hash")
	urls := splitURLList(req.FormValue("urls"))

	ids, err := b.resolveIDs(ctx, []string{hash})
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	if len(ids) == 0 {
		return errorResponse(req, http.StatusNotFound, "torrent hash not found")
	}

	args := map[string]any{"ids": ids, "trackerRemove": urls}
	if err := b.rpc.call(ctx, "torrent-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}

	return textResponse(req, "Ok.")
}

// --- transfer ---------------------------------------------------------------

func (b *Bridge) handleTransferInfo(ctx context.Context, req *http.Request) (*http.Response, error) {
	stats, err := b.fetchStats(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}
	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	return jsonResponse(req, http.StatusOK, map[string]any{
		"connection_status": "connected",
		"dht_nodes":         0,
		"dl_info_data":      stats.DownloadedBytes,
		"dl_info_speed":     0,
		"dl_rate_limit":     limitBytes(sess.SpeedLimitDownEnabled, sess.SpeedLimitDown),
		"up_info_data":      stats.UploadedBytes,
		"up_info_speed":     0,
		"up_rate_limit":     transmissionUploadRateLimit(sess),
	})
}

func (b *Bridge) handleSpeedLimitsMode(ctx context.Context, req *http.Request) (*http.Response, error) {
	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	mode := "0"
	if sess.AltSpeedEnabled {
		mode = "1"
	}
	return textResponse(req, mode)
}

func (b *Bridge) handleToggleSpeedLimits(ctx context.Context, req *http.Request) (*http.Response, error) {
	sess, err := b.getSession(ctx)
	if err != nil {
		return rpcErrorResponse(req, err)
	}

	args := map[string]any{"alt-speed-enabled": !sess.AltSpeedEnabled}
	if err := b.rpc.call(ctx, "session-set", args, nil); err != nil {
		return rpcErrorResponse(req, err)
	}
	b.invalidateSessionCache()

	return textResponse(req, "Ok.")
}

// --- data helpers -----------------------------------------------------------

// transmissionSessionFields is the allowlist of session-get/session-set
// fields qui's Transmission preferences surface reads and writes. Keys use
// the daemon's own spelling so values pass through unmodified.
var transmissionSessionFields = map[string]bool{
	// Torrents
	"download-dir":               true,
	"incomplete-dir":             true,
	"incomplete-dir-enabled":     true,
	"start-added-torrents":       true,
	"rename-partial-files":       true,
	"download-queue-enabled":     true,
	"download-queue-size":        true,
	"seed-queue-enabled":         true,
	"seed-queue-size":            true,
	"seedRatioLimit":             true,
	"seedRatioLimited":           true,
	"idle-seeding-limit":         true,
	"idle-seeding-limit-enabled": true,
	// Speed
	"speed-limit-up":           true,
	"speed-limit-up-enabled":   true,
	"speed-limit-down":         true,
	"speed-limit-down-enabled": true,
	"alt-speed-enabled":        true,
	"alt-speed-up":             true,
	"alt-speed-down":           true,
	"alt-speed-time-enabled":   true,
	"alt-speed-time-begin":     true,
	"alt-speed-time-end":       true,
	"alt-speed-time-day":       true,
	// Peers
	"peer-limit-per-torrent": true,
	"peer-limit-global":      true,
	"encryption":             true,
	"pex-enabled":            true,
	"dht-enabled":            true,
	"lpd-enabled":            true,
	"blocklist-enabled":      true,
	"blocklist-url":          true,
	"blocklist-size":         true,
	// Network
	"peer-port":                 true,
	"peer-port-random-on-start": true,
	"port-forwarding-enabled":   true,
	"utp-enabled":               true,
	"default-trackers":          true,
}

// GetSession returns the daemon's session settings filtered to the fields
// qui manages. Fields the daemon does not report are absent from the map,
// which lets the frontend hide version-specific options.
func (b *Bridge) GetSession(ctx context.Context) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := b.rpc.call(ctx, "session-get", nil, &raw); err != nil {
		return nil, err
	}

	out := make(map[string]any, len(transmissionSessionFields))
	for key, value := range raw {
		if !transmissionSessionFields[key] {
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			continue
		}
		out[key] = decoded
	}
	return out, nil
}

// SetSession applies a subset of session settings. Keys outside the
// allowlist are rejected so callers cannot mutate arbitrary daemon state.
func (b *Bridge) SetSession(ctx context.Context, settings map[string]any) error {
	args := make(map[string]any, len(settings))
	for key, value := range settings {
		if !transmissionSessionFields[key] || key == "blocklist-size" {
			return fmt.Errorf("transmission: unsupported session field %q", key)
		}
		args[key] = value
	}
	if len(args) == 0 {
		return nil
	}

	if err := b.rpc.call(ctx, "session-set", args, nil); err != nil {
		return err
	}
	b.invalidateSessionCache()
	return nil
}

// UpdateBlocklist asks the daemon to re-download its blocklist and returns
// the new rule count.
func (b *Bridge) UpdateBlocklist(ctx context.Context) (int64, error) {
	var result struct {
		BlocklistSize int64 `json:"blocklist-size"`
	}
	if err := b.rpc.call(ctx, "blocklist-update", nil, &result); err != nil {
		return 0, err
	}
	b.invalidateSessionCache()
	return result.BlocklistSize, nil
}

// fetchTorrents retrieves all torrents with the given field list.
func (b *Bridge) fetchTorrents(ctx context.Context, fields []string) ([]torrent, error) {
	var result torrentGetArguments
	args := map[string]any{"fields": fields}
	if err := b.rpc.call(ctx, "torrent-get", args, &result); err != nil {
		return nil, err
	}
	return result.Torrents, nil
}

// fetchSingleTorrent retrieves one torrent by hash (case-insensitive) or
// nil when the daemon does not know it. Some daemon builds and reverse
// proxies are unreliable at resolving hash-string ids, so a by-id miss
// falls back to fetching the full list and matching locally.
func (b *Bridge) fetchSingleTorrent(ctx context.Context, hash string, fields []string) (*torrent, error) {
	args := map[string]any{"fields": fields, "ids": []string{b.resolveHash(hash)}}
	var result torrentGetArguments
	if err := b.rpc.call(ctx, "torrent-get", args, &result); err != nil {
		return nil, err
	}
	for i := range result.Torrents {
		if normalizeHash(result.Torrents[i].HashString) == normalizeHash(hash) {
			return &result.Torrents[i], nil
		}
	}

	all, err := b.fetchTorrents(ctx, fields)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if normalizeHash(all[i].HashString) == normalizeHash(hash) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// resolveHash translates a lowercase qBittorrent hash to the daemon's
// hashString spelling using the last sync snapshot, falling back to the raw
// value.
func (b *Bridge) resolveHash(hash string) string {
	hash = normalizeHash(hash)
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.snapshot != nil {
		if raw, ok := b.snapshot.hashLookup[hash]; ok {
			return raw
		}
	}
	return hash
}

func (b *Bridge) fetchStats(ctx context.Context) (*sessionStats, error) {
	var stats sessionStats
	if err := b.rpc.call(ctx, "session-stats", nil, &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// getSession returns the cached session, refreshing when stale.
func (b *Bridge) getSession(ctx context.Context) (*session, error) {
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()

	if b.sessionCache != nil && time.Since(b.sessionFetched) < sessionCacheTTL {
		return b.sessionCache, nil
	}

	var sess session
	if err := b.rpc.call(ctx, "session-get", nil, &sess); err != nil {
		return nil, err
	}

	b.sessionCache = &sess
	b.sessionFetched = time.Now()
	return &sess, nil
}

func (b *Bridge) invalidateSessionCache() {
	b.sessionMu.Lock()
	b.sessionCache = nil
	b.sessionMu.Unlock()
}

// fetchFreeSpace returns the free space of the download dir (cached).
func (b *Bridge) fetchFreeSpace(ctx context.Context, dir string) int64 {
	if dir == "" {
		return 0
	}

	b.freeSpaceMu.Lock()
	defer b.freeSpaceMu.Unlock()

	if b.freeSpacePath == dir && time.Since(b.freeSpaceAt) < freeSpaceCacheTTL {
		return b.freeSpaceBytes
	}

	var result freeSpaceArguments
	args := map[string]any{"path": dir}
	if err := b.rpc.call(ctx, "free-space", args, &result); err != nil {
		return 0
	}

	b.freeSpacePath = dir
	b.freeSpaceBytes = result.SizeBytes
	b.freeSpaceAt = time.Now()
	return result.SizeBytes
}

// resolveIDs translates qui's lowercase qBittorrent hashes into the daemon's
// hashString spelling using the last sync snapshot, falling back to the raw
// value (the RPC also matches known hashes case-insensitively).
func (b *Bridge) resolveIDs(_ context.Context, hashes []string) ([]string, error) {
	if len(hashes) == 0 {
		return nil, nil
	}

	b.mu.Lock()
	var lookup map[string]string
	if b.snapshot != nil {
		lookup = b.snapshot.hashLookup
	}
	b.mu.Unlock()

	ids := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		hash = normalizeHash(hash)
		if hash == "" {
			continue
		}
		if lookup != nil {
			if raw, ok := lookup[hash]; ok {
				ids = append(ids, raw)
				continue
			}
		}
		ids = append(ids, hash)
	}
	return ids, nil
}

// --- response helpers --------------------------------------------------------

func jsonResponse(req *http.Request, status int, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("transmission: encode response: %w", err)
	}

	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(string(data))),
		ContentLength: int64(len(data)),
		Request:       req,
	}, nil
}

func textResponse(req *http.Request, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        http.StatusText(http.StatusOK),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"text/plain"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func errorResponse(req *http.Request, status int, message string) (*http.Response, error) {
	return jsonResponse(req, status, map[string]string{"message": message})
}

// rpcErrorResponse maps upstream RPC failures onto a 502 so the qBittorrent
// client treats them as hard errors (its retry loop treats >=500 as fatal,
// avoiding pointless retries against a down daemon).
func rpcErrorResponse(req *http.Request, err error) (*http.Response, error) {
	if err == ErrUnauthorized { //nolint:errorlint // sentinel comparison
		return errorResponse(req, http.StatusUnauthorized, "Transmission rejected the credentials")
	}
	return errorResponse(req, http.StatusBadGateway, err.Error())
}

// --- parsing helpers ----------------------------------------------------------

func normalizeHash(hash string) string {
	return strings.ToLower(strings.TrimSpace(hash))
}

// splitPipe splits qBittorrent's pipe-separated hash lists.
func splitPipe(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "|")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// splitURLList splits newline (and pipe) separated URL lists.
func splitURLList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '|'
	})
	result := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			result = append(result, f)
		}
	}
	return result
}

// splitComma splits comma separated tag lists.
func splitComma(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func hasTag(tags string, tag string) bool {
	for _, t := range strings.Split(tags, ", ") {
		if t == tag {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mapKeysMissing(has, want map[string]bool) []string {
	var missing []string
	for k := range want {
		if _, ok := has[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func trackersEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || len(va) != len(vb) {
			return false
		}
		for i := range va {
			if va[i] != vb[i] {
				return false
			}
		}
	}
	return true
}

// jsonEqual compares two JSON payloads by their exact bytes. The bridge only
// ever produces deterministic serialization for the same input state.
func jsonEqual(a, b json.RawMessage) bool {
	return string(a) == string(b)
}

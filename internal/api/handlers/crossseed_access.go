package handlers

import (
	"errors"
	"net/http"
)

var errCrossSeedInstanceForbidden = errors.New("cross-seed instance access denied")

// scopeCrossSeedInstanceIDs converts an omitted instance list into the
// current user's visible instances and rejects explicit IDs they cannot use.
// Administrators retain the service's existing empty-list semantics (all
// instances).
func (h *CrossSeedHandler) scopeCrossSeedInstanceIDs(r *http.Request, requested []int) ([]int, error) {
	if isAdmin(r) {
		return requested, nil
	}
	if h.instanceStore == nil {
		return requested, nil
	}

	if len(requested) == 0 {
		instances, err := h.instanceStore.ListForUser(r.Context(), currentUserID(r), false)
		if err != nil {
			return nil, err
		}
		if len(instances) == 0 {
			return nil, errCrossSeedInstanceForbidden
		}
		ids := make([]int, 0, len(instances))
		for _, instance := range instances {
			allowed, err := h.canAccessCrossSeedTarget(r, instance.ID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			ids = append(ids, instance.ID)
		}
		if len(ids) == 0 {
			return nil, errCrossSeedInstanceForbidden
		}
		return ids, nil
	}

	seen := make(map[int]struct{}, len(requested))
	ids := make([]int, 0, len(requested))
	for _, id := range requested {
		if id <= 0 {
			return nil, errCrossSeedInstanceForbidden
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		allowed, err := h.instanceStore.CanAccess(r.Context(), id, currentUserID(r), false)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, errCrossSeedInstanceForbidden
		}
		allowed, err = h.canAccessCrossSeedTarget(r, id)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, errCrossSeedInstanceForbidden
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// A routed add writes to the target even when the request names only its source.
func (h *CrossSeedHandler) canAccessCrossSeedTarget(r *http.Request, sourceID int) (bool, error) {
	if h.routes == nil {
		return true, nil
	}
	route, err := h.routes.CrossSeedRouteStore.Get(r.Context(), sourceID)
	if err != nil {
		return false, err
	}
	if route == nil {
		return true, nil
	}
	return h.instanceStore.CanAccess(r.Context(), route.CrossSeedInstanceID, currentUserID(r), isAdmin(r))
}

func respondCrossSeedInstanceScopeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errCrossSeedInstanceForbidden) {
		RespondError(w, http.StatusForbidden, "Instance access denied")
		return
	}
	RespondError(w, http.StatusInternalServerError, "Failed to authorize instances")
}

// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package handlers

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubBinaryInstaller struct {
	called bool
	err    error
}

func (*stubBinaryInstaller) IsSupported() bool { return true }
func (s *stubBinaryInstaller) Install(io.Reader) error {
	s.called = true
	return s.err
}

func TestUploadedUpdateSharesRestartLock(t *testing.T) {
	for _, test := range []struct {
		name       string
		busy       bool
		installErr error
		restartErr error
		status     int
	}{
		{name: "installed", status: http.StatusAccepted},
		{name: "busy", busy: true, status: http.StatusConflict},
		{name: "invalid binary", installErr: errors.New("invalid binary"), status: http.StatusBadRequest},
		{name: "restart refused", restartErr: errors.New("not executable"), status: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			installer := &stubBinaryInstaller{err: test.installErr}
			restarter := &stubRestarter{err: test.restartErr}
			if test.busy {
				restarter.Lock()
			}
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			file, err := form.CreateFormFile("binary", "qui")
			require.NoError(t, err)
			_, err = file.Write([]byte("synthetic binary"))
			require.NoError(t, err)
			require.NoError(t, form.Close())
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/update/upload", &body)
			req.Header.Set("Content-Type", form.FormDataContentType())
			rec := httptest.NewRecorder()
			NewUpdateHandler(installer, restarter).UploadBinary(rec, req)
			require.Equal(t, test.status, rec.Code, rec.Body.String())
			require.Equal(t, !test.busy, installer.called)
			locked := !restarter.TryLock()
			require.Equal(t, test.busy || test.status == http.StatusAccepted, locked)
			restarter.Unlock()
			if test.status == http.StatusAccepted {
				require.Equal(t, 1, restarter.requests)
				require.JSONEq(t, `{"restarting":true}`, rec.Body.String())
			} else if test.installErr != nil || test.busy {
				require.Zero(t, restarter.requests)
			}
		})
	}
}

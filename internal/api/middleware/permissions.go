// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package middleware

import (
	"net/http"

	"github.com/autobrr/qui/internal/api/ctxkeys"
	"github.com/autobrr/qui/internal/auth"
	"github.com/autobrr/qui/internal/models"
)

func RequireUserPermission(authService *auth.Service, permission models.UserPermission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := r.Context().Value(ctxkeys.UserRole).(string)
			if role == string(models.UserRoleAdmin) {
				next.ServeHTTP(w, r)
				return
			}

			userID, _ := r.Context().Value(ctxkeys.UserID).(int)
			allowed, err := authService.HasPermission(r.Context(), userID, models.UserRole(role), permission)
			if err != nil {
				http.Error(w, "Failed to verify permission", http.StatusInternalServerError)
				return
			}
			if !allowed {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

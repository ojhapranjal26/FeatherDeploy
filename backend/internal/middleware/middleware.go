package middleware

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/auth"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/model"
)

type contextKey string

const (
	ctxClaims     contextKey = "claims"
	ctxMemberRole contextKey = "memberRole"
)

// Authenticate extracts and validates the Bearer JWT.
// For SSE/EventSource clients that cannot set headers, also accepts a ?token= query param.
// If db is non-nil and the token carries a jti (session ID), the session is checked
// against the user_sessions table to support revocation (device logout).
func Authenticate(secret string, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var token string
			hdr := r.Header.Get("Authorization")
			if strings.HasPrefix(hdr, "Bearer ") {
				token = strings.TrimPrefix(hdr, "Bearer ")
			} else if q := r.URL.Query().Get("token"); q != "" {
				// Fallback for EventSource / SSE clients that cannot set Authorization header
				token = q
			}
			if token == "" {
				writeErr(w, http.StatusUnauthorized, "missing or malformed Authorization header")
				return
			}
			claims, err := auth.ParseToken(secret, token)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			// If the token carries a session ID (jti), verify it has not been revoked.
			// Tokens issued before session tracking was added have no ID and are let through.
			if db != nil && claims.ID != "" {
				var revoked int
				err := db.QueryRowContext(r.Context(),
					`SELECT revoked FROM user_sessions WHERE id=?`, claims.ID,
				).Scan(&revoked)
				if err == sql.ErrNoRows || revoked == 1 {
					writeErr(w, http.StatusUnauthorized, "session revoked or not found")
					return
				}
				if err != nil {
					writeErr(w, http.StatusInternalServerError, "internal error")
					return
				}
				// Update last_seen asynchronously so it doesn't add request latency
				go db.ExecContext(context.Background(), //nolint
					`UPDATE user_sessions SET last_seen=datetime('now') WHERE id=?`, claims.ID)
			}
			ctx := context.WithValue(r.Context(), ctxClaims, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaims retrieves token claims from context (must be behind Authenticate).
func GetClaims(ctx context.Context) *auth.Claims {
	c, _ := ctx.Value(ctxClaims).(*auth.Claims)
	return c
}

// RequireRole allows requests only if the caller's global role is in allowed.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	set := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		set[r] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil || !set[claims.Role] {
				writeErr(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireProjectAccess verifies the user is at least a viewer of the project.
// It reads {projectID} from the URL and stores the member role in context.
// superadmin and admin bypass the membership check.
func RequireProjectAccess(db *sql.DB, minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeErr(w, http.StatusUnauthorized, "unauthenticated")
				return
			}

			// superadmin / admin have full access to all projects
			if claims.Role == model.RoleSuperAdmin || claims.Role == model.RoleAdmin {
				ctx := context.WithValue(r.Context(), ctxMemberRole, "owner")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			projectIDStr := r.PathValue("projectID")
			if projectIDStr == "" {
				writeErr(w, http.StatusBadRequest, "missing projectID")
				return
			}
			var projectID int64
			if _, err := fmt.Sscanf(projectIDStr, "%d", &projectID); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid projectID")
				return
			}

			var memberRole string
			err := db.QueryRowContext(r.Context(),
				`SELECT role FROM project_members WHERE project_id=? AND user_id=?`,
				projectID, claims.UserID,
			).Scan(&memberRole)
			if err == sql.ErrNoRows {
				writeErr(w, http.StatusForbidden, "not a member of this project")
				return
			}
			if err != nil {
				slog.Error("project membership check", "err", err)
				writeErr(w, http.StatusInternalServerError, "internal error")
				return
			}

			if !roleGTE(memberRole, minRole) {
				writeErr(w, http.StatusForbidden, "insufficient project role")
				return
			}

			ctx := context.WithValue(r.Context(), ctxMemberRole, memberRole)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetMemberRole returns the caller's project-level role from context.
func GetMemberRole(ctx context.Context) string {
	r, _ := ctx.Value(ctxMemberRole).(string)
	return r
}

// roleGTE returns true if actual >= minimum in the owner>editor>viewer order.
func roleGTE(actual, minimum string) bool {
	order := map[string]int{"owner": 3, "editor": 2, "viewer": 1}
	return order[actual] >= order[minimum]
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(`{"error":"` + jsonEscape(msg) + `"}`))
}

func jsonEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}


package router

import (
	"strings"

	"golang.org/x/crypto/bcrypt"

	restful "github.com/emicklei/go-restful/v3"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/auth"
	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
	"github.com/kuzane/alertmesh/internal/sysconfig"
)

type systemHandler struct {
	db     *gorm.DB
	jwtSvc *auth.JWTService
	syscfg *sysconfig.Service
}

func newSystemHandler(db *gorm.DB, jwtSvc *auth.JWTService, syscfg *sysconfig.Service) *systemHandler {
	return &systemHandler{db: db, jwtSvc: jwtSvc, syscfg: syscfg}
}

func (h *systemHandler) registerRoutes(ws *restful.WebService) {
	// Public – no auth, no ACL
	ws.Route(ws.GET("/auth/public-key").
		To(h.getPublicKey).
		Doc("Get RSA public key for password encryption"))

	ws.Route(ws.POST("/auth/login").
		To(h.login).
		Doc("User login"))

	// User info (auth required, no ACL)
	ws.Route(ws.GET("/user/info").
		To(h.userInfo).
		Doc("Get current user info and permissions").
		Metadata(label.MetaAuth, label.Enable))

	// User management
	ws.Route(ws.GET("/users").
		To(h.listUsers).
		Doc("List users").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "User").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/users").
		To(h.createUser).
		Doc("Create a local user").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "User").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.PUT("/users/{id}").
		To(h.updateUser).
		Doc("Update user info or roles").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "User").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.DELETE("/users/{id}").
		To(h.deleteUser).
		Doc("Delete a user").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "User").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	// Role management
	ws.Route(ws.GET("/roles").
		To(h.listRoles).
		Doc("List roles with endpoints").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Role").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.PUT("/roles/{id}/endpoints").
		To(h.updateRoleEndpoints).
		Doc("Replace the permission endpoints bound to a role").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Role").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	// Endpoint list (for role assignment UI)
	ws.Route(ws.GET("/endpoints").
		To(h.listEndpoints).
		Doc("List all registered permission endpoints").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Endpoint").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	// System config (generic k/v, non-secret)
	ws.Route(ws.GET("/configs").
		To(h.listConfigs).
		Doc("List non-secret system configs").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Config").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.PUT("/configs").
		To(h.updateConfig).
		Doc("Update a system config value").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Config").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	// Auth provider configuration
	ws.Route(ws.GET("/configs/auth").
		To(h.getAuthConfig).
		Doc("Get current authentication mode").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Config").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.PUT("/configs/auth").
		To(h.setAuthConfig).
		Doc("Set authentication mode and provider config (ldap/oidc config is encrypted at rest)").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Config").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	// Oncall
	ws.Route(ws.GET("/oncall").
		To(h.listOncall).
		Doc("List oncall schedules").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Oncall").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	// Reports
	ws.Route(ws.GET("/reports/overview").
		To(h.reportOverview).
		Doc("Get overview report").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "Report").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))
}

// ─── auth ────────────────────────────────────────────────────────────────────

func (h *systemHandler) getPublicKey(_ *restful.Request, resp *restful.Response) {
	httputil.Success(resp, map[string]string{"public_key": auth.GetPublicKeyPEM()})
}

func (h *systemHandler) login(req *restful.Request, resp *restful.Response) {
	type loginReq struct {
		Username string `json:"username"`
		// Password is RSA-PKCS1v15 encrypted and base64-standard-encoded by the
		// browser using the public key from GET /auth/public-key.
		Password string `json:"password"`
	}
	var body loginReq
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}

	// Decrypt the RSA-encrypted password sent by the browser.
	plainPassword, err := auth.DecryptCipher(body.Password)
	if err != nil {
		httputil.BadRequest(resp, "invalid password encoding")
		return
	}

	localAuth := auth.NewLocalAuth(h.db)
	user, err := localAuth.Authenticate(req.Request.Context(), body.Username, plainPassword)
	if err != nil {
		httputil.Unauthorized(resp)
		return
	}

	roleNames := make([]string, 0, len(user.Roles))
	for _, r := range user.Roles {
		roleNames = append(roleNames, r.Name)
	}

	token, err := h.jwtSvc.GenerateToken(user.ID, user.Username, roleNames)
	if err != nil {
		httputil.InternalError(resp, "failed to generate token")
		return
	}

	httputil.Success(resp, map[string]string{"token": token})
}

// ─── user ────────────────────────────────────────────────────────────────────

// safeUser is the API-facing user representation with sensitive fields masked.
type safeUser struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email"` // masked
	DisplayName string     `json:"display_name"`
	Source      string     `json:"source"`
	IsActive    bool       `json:"is_active"`
	Roles       []safeRole `json:"roles,omitempty"`
}

type safeRole struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

func toSafeUser(u model.User) safeUser {
	roles := make([]safeRole, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, safeRole{ID: r.ID, Name: r.Name})
	}
	return safeUser{
		ID:          u.ID,
		Username:    u.Username,
		Email:       httputil.MaskEmail(u.Email),
		DisplayName: u.DisplayName,
		Source:      u.Source,
		IsActive:    u.IsActive,
		Roles:       roles,
	}
}

func (h *systemHandler) userInfo(req *restful.Request, resp *restful.Response) {
	username, _ := req.Attribute("username").(string)
	roles, _ := req.Attribute("roles").([]string)
	permissions := h.resolvePermissions(roles)

	httputil.Success(resp, map[string]interface{}{
		"username":    username,
		"roles":       roles,
		"permissions": permissions,
	})
}

func (h *systemHandler) resolvePermissions(roles []string) []string {
	for _, r := range roles {
		if r == "admin" || r == "superadmin" {
			return []string{"*"}
		}
	}

	var endpoints []model.Endpoint
	h.db.Joins("JOIN role_endpoints ON role_endpoints.endpoint_identity = endpoints.identity").
		Joins("JOIN roles ON roles.id = role_endpoints.role_id").
		Where("roles.name IN ?", roles).
		Find(&endpoints)

	seen := make(map[string]bool)
	var perms []string
	for _, ep := range endpoints {
		if !seen[ep.Identity] {
			seen[ep.Identity] = true
			perms = append(perms, ep.Identity)
		}
	}
	return perms
}

func (h *systemHandler) listUsers(req *restful.Request, resp *restful.Response) {
	var users []model.User
	h.db.WithContext(req.Request.Context()).Preload("Roles").Find(&users)

	safe := make([]safeUser, 0, len(users))
	for _, u := range users {
		safe = append(safe, toSafeUser(u))
	}
	httputil.Success(resp, safe)
}

type createUserReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	RoleIDs     []uint `json:"role_ids"`
}

func (h *systemHandler) createUser(req *restful.Request, resp *restful.Response) {
	var body createUserReq
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	if body.Username == "" || body.Password == "" {
		httputil.BadRequest(resp, "username and password are required")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		httputil.InternalError(resp, "failed to hash password")
		return
	}
	u := model.User{
		Username:     body.Username,
		DisplayName:  body.DisplayName,
		Email:        body.Email,
		PasswordHash: string(hash),
		Source:       "local",
		IsActive:     true,
	}
	if res := h.db.WithContext(req.Request.Context()).Create(&u); res.Error != nil {
		httputil.InternalError(resp, "failed to create user: "+res.Error.Error())
		return
	}
	if len(body.RoleIDs) > 0 {
		var roles []*model.Role
		h.db.WithContext(req.Request.Context()).Where("id IN ?", body.RoleIDs).Find(&roles)
		_ = h.db.WithContext(req.Request.Context()).Model(&u).Association("Roles").Replace(roles)
	}
	h.db.WithContext(req.Request.Context()).Preload("Roles").First(&u, "id = ?", u.ID)
	httputil.Success(resp, toSafeUser(u))
}

type updateUserReq struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	IsActive    *bool  `json:"is_active"`
	RoleIDs     []uint `json:"role_ids"`
}

func (h *systemHandler) updateUser(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	var u model.User
	if res := h.db.WithContext(req.Request.Context()).Preload("Roles").First(&u, "id = ?", id); res.Error != nil {
		httputil.NotFound(resp)
		return
	}
	var body updateUserReq
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	if body.DisplayName != "" {
		u.DisplayName = body.DisplayName
	}
	if body.Email != "" {
		u.Email = body.Email
	}
	if body.IsActive != nil {
		u.IsActive = *body.IsActive
	}
	if body.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			httputil.InternalError(resp, "failed to hash password")
			return
		}
		u.PasswordHash = string(hash)
	}
	h.db.WithContext(req.Request.Context()).Save(&u)
	if body.RoleIDs != nil {
		var roles []*model.Role
		h.db.WithContext(req.Request.Context()).Where("id IN ?", body.RoleIDs).Find(&roles)
		_ = h.db.WithContext(req.Request.Context()).Model(&u).Association("Roles").Replace(roles)
	}
	h.db.WithContext(req.Request.Context()).Preload("Roles").First(&u, "id = ?", u.ID)
	httputil.Success(resp, toSafeUser(u))
}

func (h *systemHandler) deleteUser(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	var u model.User
	if res := h.db.WithContext(req.Request.Context()).First(&u, "id = ?", id); res.Error != nil {
		httputil.NotFound(resp)
		return
	}
	if u.Username == "admin" {
		httputil.BadRequest(resp, "cannot delete the built-in admin account")
		return
	}
	_ = h.db.WithContext(req.Request.Context()).Model(&u).Association("Roles").Clear()
	h.db.WithContext(req.Request.Context()).Delete(&u)
	httputil.Success(resp, map[string]string{"id": id})
}

func (h *systemHandler) listRoles(req *restful.Request, resp *restful.Response) {
	var roles []model.Role
	h.db.WithContext(req.Request.Context()).Preload("Endpoints").Find(&roles)
	httputil.Success(resp, roles)
}

func (h *systemHandler) updateRoleEndpoints(req *restful.Request, resp *restful.Response) {
	idStr := req.PathParameter("id")
	var role model.Role
	if res := h.db.WithContext(req.Request.Context()).First(&role, "id = ?", idStr); res.Error != nil {
		httputil.NotFound(resp)
		return
	}
	var body struct {
		Identities []string `json:"identities"` // list of endpoint identity strings
	}
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	var endpoints []*model.Endpoint
	if len(body.Identities) > 0 {
		h.db.WithContext(req.Request.Context()).Where("identity IN ?", body.Identities).Find(&endpoints)
	}
	if err := h.db.WithContext(req.Request.Context()).Model(&role).Association("Endpoints").Replace(endpoints); err != nil {
		httputil.InternalError(resp, "failed to update role endpoints")
		return
	}
	h.db.WithContext(req.Request.Context()).Preload("Endpoints").First(&role, "id = ?", role.ID)
	httputil.Success(resp, role)
}

func (h *systemHandler) listEndpoints(req *restful.Request, resp *restful.Response) {
	var endpoints []model.Endpoint
	h.db.WithContext(req.Request.Context()).Find(&endpoints)
	httputil.Success(resp, endpoints)
}

// ─── generic config ──────────────────────────────────────────────────────────

// listConfigs returns only the non-secret keys (excludes security.* and auth.ldap/oidc).
func (h *systemHandler) listConfigs(req *restful.Request, resp *restful.Response) {
	var configs []model.SystemConfig
	h.db.WithContext(req.Request.Context()).
		Where("key NOT LIKE 'security.%' AND key NOT IN ('auth.ldap','auth.oidc')").
		Find(&configs)
	httputil.Success(resp, configs)
}

func (h *systemHandler) updateConfig(req *restful.Request, resp *restful.Response) {
	var cfg model.SystemConfig
	if err := req.ReadEntity(&cfg); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	// Prevent direct writes to secret keys via this generic endpoint.
	if cfg.Key == sysconfig.KeyJWTSecret || cfg.Key == sysconfig.KeyAuthLDAP || cfg.Key == sysconfig.KeyAuthOIDC {
		httputil.BadRequest(resp, "use /configs/auth to manage sensitive configuration")
		return
	}
	cfg.Value = strings.TrimSpace(cfg.Value)
	if cfg.Value == "" {
		httputil.BadRequest(resp, "value is required")
		return
	}
	if err := h.db.WithContext(req.Request.Context()).Save(&cfg).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, cfg)
}

// ─── auth config ─────────────────────────────────────────────────────────────

func (h *systemHandler) getAuthConfig(req *restful.Request, resp *restful.Response) {
	mode := h.syscfg.AuthMode(req.Request.Context())
	httputil.Success(resp, map[string]string{"mode": mode})
}

type setAuthConfigReq struct {
	// Mode is one of: local | ldap | oidc
	Mode string `json:"mode"`
	// Config holds the provider-specific JSON payload.
	// For "ldap": { host, port, base_dn, bind_dn, bind_password, user_filter, tls }
	// For "oidc": { issuer, client_id, client_secret, redirect_uri, scopes[] }
	// Omit for "local".  The value is encrypted before storage.
	Config string `json:"config,omitempty"`
}

func (h *systemHandler) setAuthConfig(req *restful.Request, resp *restful.Response) {
	var body setAuthConfigReq
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	switch body.Mode {
	case "local", "ldap", "oidc":
	default:
		httputil.BadRequest(resp, "mode must be one of: local, ldap, oidc")
		return
	}
	// Walk the JSON config and RSA-decrypt any field the browser wrapped with
	// auth.ENCPrefix (e.g. ldap.bind_password / oidc.client_secret) before it
	// is AES-encrypted at rest by sysconfig.SetAuthMode.
	configJSON := auth.DecodeJSONClientCiphers(body.Config)
	if err := h.syscfg.SetAuthMode(req.Request.Context(), body.Mode, configJSON); err != nil {
		httputil.InternalError(resp, "failed to update auth config")
		return
	}
	httputil.Success(resp, map[string]string{"mode": body.Mode})
}

// ─── oncall / reports ────────────────────────────────────────────────────────

func (h *systemHandler) listOncall(req *restful.Request, resp *restful.Response) {
	var schedules []model.OncallSchedule
	h.db.WithContext(req.Request.Context()).Find(&schedules)
	httputil.Success(resp, schedules)
}

func (h *systemHandler) reportOverview(req *restful.Request, resp *restful.Response) {
	httputil.Success(resp, map[string]string{"status": "not yet implemented"})
}

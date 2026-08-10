package router

// CRUD handlers for /api/v1/alert/webhook-sources, the management surface for
// trusted external sources that POST signed alerts to /alerts/webhook/{source}.
//
// Key handling rules (security-critical):
//
//   - Each source owns one Ed25519 keypair.
//   - The PRIVATE key is generated on the server, returned in the response of
//     `create` and `rotate` exactly ONCE, and is NEVER stored. Operators are
//     expected to copy it into their external alert source (script, adapter)
//     and never paste it back.
//   - Rotating the keypair immediately invalidates all previous signatures —
//     there is no grace period in this iteration (deliberate; see plan
//     "Out of scope" section).
//   - `update` changes description / allow_skew / is_enabled / name / mapping
//     (JSON gjson paths for webhook body → RawAlert). Renames change `{source}`.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	restful "github.com/emicklei/go-restful/v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
)

// webhookSourceCreated is the response payload returned ONLY on create /
// rotate; the caller MUST persist `private_key_pem` immediately because it is
// not stored on the server and cannot be retrieved later.
type webhookSourceCreated struct {
	model.WebhookSource
	PrivateKeyPEM string `json:"private_key_pem"`
}

// registerWebhookSourceRoutes wires the 5 endpoints onto the shared WebService.
// Called from alertCenterHandler.registerRoutes so admins can browse all
// alert-center configuration in one place.
func (h *alertCenterHandler) registerWebhookSourceRoutes(ws *restful.WebService) {
	ws.Route(ws.GET("/alert/webhook-sources").To(h.listWebhookSources).
		Doc("List trusted webhook sources (no private keys)").
		Metadata(label.MetaIdentity, label.WebhookSourceAccess).
		Metadata(label.MetaModule, label.AlertCenterModuleName).
		Metadata(label.MetaKind, "WebhookSource").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/alert/webhook-sources").To(h.createWebhookSource).
		Doc("Create webhook source; returns one-time private key").
		Metadata(label.MetaIdentity, label.WebhookSourceAccess).
		Metadata(label.MetaModule, label.AlertCenterModuleName).
		Metadata(label.MetaKind, "WebhookSource").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.PUT("/alert/webhook-sources/{id}").To(h.updateWebhookSource).
		Doc("Update webhook source metadata (no key changes)").
		Metadata(label.MetaIdentity, label.WebhookSourceAccess).
		Metadata(label.MetaModule, label.AlertCenterModuleName).
		Metadata(label.MetaKind, "WebhookSource").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/alert/webhook-sources/{id}/rotate").To(h.rotateWebhookSource).
		Doc("Regenerate keypair; returns one-time private key").
		Metadata(label.MetaIdentity, label.WebhookSourceAccess).
		Metadata(label.MetaModule, label.AlertCenterModuleName).
		Metadata(label.MetaKind, "WebhookSource").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.DELETE("/alert/webhook-sources/{id}").To(h.deleteWebhookSource).
		Doc("Soft-delete webhook source").
		Metadata(label.MetaIdentity, label.WebhookSourceAccess).
		Metadata(label.MetaModule, label.AlertCenterModuleName).
		Metadata(label.MetaKind, "WebhookSource").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))
}

func (h *alertCenterHandler) listWebhookSources(req *restful.Request, resp *restful.Response) {
	var rows []model.WebhookSource
	if err := h.db.WithContext(req.Request.Context()).
		Order("created_at desc").Find(&rows).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, rows)
}

func (h *alertCenterHandler) createWebhookSource(req *restful.Request, resp *restful.Response) {
	var in model.WebhookSource
	if err := req.ReadEntity(&in); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	if in.Name == "" {
		httputil.BadRequest(resp, "name is required")
		return
	}
	if in.AllowSkew <= 0 {
		in.AllowSkew = 300
	}

	row := model.WebhookSource{
		Name:        in.Name,
		AllowSkew:   in.AllowSkew,
		IsEnabled:   true,
		IsUnsigned:  in.IsUnsigned,
		Description: in.Description,
		Mapping:     normaliseWebhookMappingJSON(in.Mapping),
	}

	// Unsigned sources use webhook-plain endpoint, no keypair needed.
	if !in.IsUnsigned {
		clientID, err := newClientID()
		if err != nil {
			httputil.InternalError(resp, "failed to generate client_id: "+err.Error())
			return
		}
		pubPEM, privPEM, err := generateEd25519Keypair()
		if err != nil {
			httputil.InternalError(resp, "failed to generate keypair: "+err.Error())
			return
		}
		row.ClientID = clientID
		row.PublicKey = pubPEM
		row.ID = ""
		if err := h.db.WithContext(req.Request.Context()).Create(&row).Error; err != nil {
			httputil.InternalError(resp, err.Error())
			return
		}
		httputil.Created(resp, webhookSourceCreated{WebhookSource: row, PrivateKeyPEM: privPEM})
		return
	}

	// Unsigned path: no key material.
	row.ID = ""
	if err := h.db.WithContext(req.Request.Context()).Create(&row).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Created(resp, row)
}

func (h *alertCenterHandler) updateWebhookSource(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	var existing model.WebhookSource
	if err := h.db.WithContext(req.Request.Context()).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}

	var in model.WebhookSource
	if err := req.ReadEntity(&in); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}

	// Only mutable metadata fields propagate. Keys / client_id are managed via
	// the rotate endpoint; name uniqueness is enforced by the unique index.
	if in.Name != "" {
		existing.Name = in.Name
	}
	if in.AllowSkew > 0 {
		existing.AllowSkew = in.AllowSkew
	}
	existing.IsEnabled = in.IsEnabled
	existing.IsUnsigned = in.IsUnsigned
	existing.Description = in.Description
	if len(in.Mapping) > 0 {
		existing.Mapping = normaliseWebhookMappingJSON(in.Mapping)
	}

	if err := h.db.WithContext(req.Request.Context()).Save(&existing).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, existing)
}

func (h *alertCenterHandler) rotateWebhookSource(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	var existing model.WebhookSource
	if err := h.db.WithContext(req.Request.Context()).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}

	pubPEM, privPEM, err := generateEd25519Keypair()
	if err != nil {
		httputil.InternalError(resp, "failed to generate keypair: "+err.Error())
		return
	}
	// client_id is also rotated so that any cached signatures from the old key
	// fail with a clean "unknown keyid" rather than "bad signature".
	newCID, err := newClientID()
	if err != nil {
		httputil.InternalError(resp, "failed to generate client_id: "+err.Error())
		return
	}
	existing.PublicKey = pubPEM
	existing.ClientID = newCID
	if err := h.db.WithContext(req.Request.Context()).Save(&existing).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, webhookSourceCreated{WebhookSource: existing, PrivateKeyPEM: privPEM})
}

func (h *alertCenterHandler) deleteWebhookSource(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	if err := h.db.WithContext(req.Request.Context()).
		Delete(&model.WebhookSource{}, "id = ?", id).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, nil)
}

func normaliseWebhookMappingJSON(j datatypes.JSON) datatypes.JSON {
	if len(j) == 0 {
		return datatypes.JSON([]byte("{}"))
	}
	s := strings.TrimSpace(string(j))
	if s == "" || s == "null" {
		return datatypes.JSON([]byte("{}"))
	}
	return j
}

// generateEd25519Keypair returns PEM-encoded (PKIX public, PKCS#8 private)
// strings suitable for round-tripping through pem.Decode + x509.ParseXxx.
func generateEd25519Keypair() (publicPEM, privatePEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("ed25519.GenerateKey: %w", err)
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("MarshalPKIXPublicKey: %w", err)
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("MarshalPKCS8PrivateKey: %w", err)
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}))
	privatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}))
	return publicPEM, privatePEM, nil
}

// newClientID returns a 16-hex-char id with a "ws_" prefix, used as the
// RFC 9421 `keyid` parameter clients embed in Signature-Input.
func newClientID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ws_" + hex.EncodeToString(b[:]), nil
}

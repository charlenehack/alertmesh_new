package k8scache

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	cfgpkg "github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/model"
)

// Manager manages ClusterCache instances for all k8s data sources.
type Manager struct {
	db     *gorm.DB
	cfg    *cfgpkg.Config
	mu     sync.RWMutex
	caches map[string]*ClusterCache // dsID → ClusterCache
}

// NewManager creates a new Manager and loads all enabled k8s data sources.
func NewManager(db *gorm.DB, cfg *cfgpkg.Config) *Manager {
	m := &Manager{
		db:     db,
		cfg:    cfg,
		caches: make(map[string]*ClusterCache),
	}
	m.loadAll()
	return m
}

// loadAll loads all enabled k8s data sources and starts Informers.
func (m *Manager) loadAll() {
	var rows []model.DataSource
	if err := m.db.Where("kind = ? AND is_enabled = ?", model.DataSourceKindK8s, true).Find(&rows).Error; err != nil {
		log.Error().Err(err).Msg("k8scache: load data sources failed")
		return
	}

	for _, row := range rows {
		m.startCache(row)
	}

	log.Info().Int("count", len(rows)).Msg("k8scache: loaded clusters")
}

// startCache builds config and starts a ClusterCache for a data source.
func (m *Manager) startCache(row model.DataSource) {
	ccCfg := ClusterConfig{
		InCluster: false,
	}

	cfgMap := map[string]any{}
	_ = json.Unmarshal(row.Config, &cfgMap)
	if v, ok := cfgMap["in_cluster"]; ok {
		if b, ok := v.(bool); ok {
			ccCfg.InCluster = b
		}
	}
	if v, ok := cfgMap["tls_insecure_skip_verify"]; ok {
		if b, ok := v.(bool); ok {
			ccCfg.TLSInsecureSkipVerify = b
		}
	}

	if !ccCfg.InCluster {
		ccCfg.BaseURL = row.Endpoint
		// Decrypt secrets
		secrets := map[string]string{}
		if row.SecretEnc != "" && m.cfg != nil && m.cfg.EncryptionKey != "" {
			plain, err := cfgpkg.Decrypt(row.SecretEnc, m.cfg.EncryptionKey)
			if err != nil {
				log.Error().Err(err).Str("ds", row.ID).Msg("k8scache: decrypt secrets failed, skipping cluster")
				return
			}
			_ = json.Unmarshal([]byte(plain), &secrets)
		}
		ccCfg.Token = secrets["token"]
	}

	cache, err := NewClusterCache(row.ID, row.Name, ccCfg)
	if err != nil {
		log.Error().Err(err).Str("ds", row.ID).Str("name", row.Name).Msg("k8scache: start cache failed")
		return
	}

	m.mu.Lock()
	m.caches[row.ID] = cache
	m.mu.Unlock()

	log.Info().Str("ds", row.ID).Str("name", row.Name).Msg("k8scache: started")
}

// GetCache returns the ClusterCache for a data source ID.
func (m *Manager) GetCache(dsID string) *ClusterCache {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.caches[dsID]
}

// Start adds a new cluster cache (called when a k8s data source is created/enabled).
func (m *Manager) Start(dsID string) {
	var row model.DataSource
	if err := m.db.Where("id = ? AND kind = ? AND is_enabled = ?", dsID, model.DataSourceKindK8s, true).First(&row).Error; err != nil {
		log.Error().Err(err).Str("ds", dsID).Msg("k8scache: data source not found")
		return
	}

	// Stop existing cache if any
	m.Stop(dsID)

	m.startCache(row)
}

// Stop removes and stops a cluster cache.
func (m *Manager) Stop(dsID string) {
	m.mu.Lock()
	cache, ok := m.caches[dsID]
	if ok {
		delete(m.caches, dsID)
	}
	m.mu.Unlock()

	if ok {
		cache.Stop()
	}
}

// SearchPods is a convenience method that looks up the cache and searches pods.
func (m *Manager) SearchPods(dsID string, params SearchParams) (PaginateResult, error) {
	cache := m.GetCache(dsID)
	if cache == nil {
		return PaginateResult{}, fmt.Errorf("cluster %s not cached", dsID)
	}
	if !cache.Ready() {
		return PaginateResult{}, fmt.Errorf("cluster %s cache not ready yet", dsID)
	}
	return cache.SearchPods(params), nil
}

// SearchNodes is a convenience method for node search.
func (m *Manager) SearchNodes(dsID string, params SearchParams) (PaginateResult, error) {
	cache := m.GetCache(dsID)
	if cache == nil {
		return PaginateResult{}, fmt.Errorf("cluster %s not cached", dsID)
	}
	if !cache.Ready() {
		return PaginateResult{}, fmt.Errorf("cluster %s cache not ready yet", dsID)
	}
	return cache.SearchNodes(params), nil
}

// SearchGeneric is a convenience method for generic resource search.
func (m *Manager) SearchGeneric(dsID string, res ResourceType, params SearchParams) (PaginateResult, error) {
	cache := m.GetCache(dsID)
	if cache == nil {
		return PaginateResult{}, fmt.Errorf("cluster %s not cached", dsID)
	}
	if !cache.Ready() {
		return PaginateResult{}, fmt.Errorf("cluster %s cache not ready yet", dsID)
	}
	return cache.SearchGeneric(res, params), nil
}

// Shutdown stops all cluster caches.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	caches := make([]*ClusterCache, 0, len(m.caches))
	for id, cache := range m.caches {
		caches = append(caches, cache)
		delete(m.caches, id)
	}
	m.mu.Unlock()

	for _, cache := range caches {
		cache.Stop()
	}
	log.Info().Int("count", len(caches)).Msg("k8scache: all caches stopped")
}

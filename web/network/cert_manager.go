package network

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/util/common"
)

// CertManager keeps the panel certificate in memory and reloads it when the
// files change, so renewals take effect without restarting the panel (and Xray).
type CertManager struct {
	mu       sync.RWMutex
	domain   string
	getPaths func() (string, string)

	cert     *tls.Certificate
	leaf     *x509.Certificate
	certPath string
	keyPath  string
	certMod  time.Time
	keyMod   time.Time

	stopCh  chan struct{}
	stopped bool
}

func NewCertManager(getPaths func() (string, string), domain string) *CertManager {
	return &CertManager{
		domain:   domain,
		getPaths: getPaths,
		stopCh:   make(chan struct{}),
	}
}

func (m *CertManager) Start() {
	m.reload(true)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.reload(false)
			case <-m.stopCh:
				return
			}
		}
	}()
}

func (m *CertManager) Stop() {
	m.mu.Lock()
	if !m.stopped {
		m.stopped = true
		close(m.stopCh)
	}
	m.mu.Unlock()
}

var (
	activeCertMu sync.RWMutex
	activeCert   *CertManager
)

// SetActiveCertManager registers the panel's certificate manager so settings
// changes can refresh it without waiting for the periodic reload.
func SetActiveCertManager(m *CertManager) {
	activeCertMu.Lock()
	activeCert = m
	activeCertMu.Unlock()
}

// ReloadPanelCert forces an immediate certificate reload after the panel
// certificate settings or files change. It is a no-op before the manager is
// registered.
func ReloadPanelCert() {
	activeCertMu.RLock()
	m := activeCert
	activeCertMu.RUnlock()
	if m != nil {
		go m.reload(true)
	}
}

func (m *CertManager) reload(force bool) {
	certPath, keyPath := m.getPaths()
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)

	if certPath == "" || keyPath == "" {
		m.mu.Lock()
		hadCert := m.cert != nil
		m.cert = nil
		m.leaf = nil
		m.certPath, m.keyPath = "", ""
		m.mu.Unlock()
		if hadCert {
			logger.Warning("Panel certificate cleared, falling back to HTTP")
		}
		return
	}

	certStat, err := os.Stat(certPath)
	if err != nil {
		return
	}
	keyStat, err := os.Stat(keyPath)
	if err != nil {
		return
	}

	m.mu.RLock()
	samePaths := m.certPath == certPath && m.keyPath == keyPath
	sameFiles := samePaths && m.certMod.Equal(certStat.ModTime()) && m.keyMod.Equal(keyStat.ModTime())
	m.mu.RUnlock()
	if !force && sameFiles {
		return
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		logger.Warning("Failed to load panel certificate, keeping the current one:", err)
		return
	}
	if len(cert.Certificate) == 0 {
		logger.Warning("Panel certificate file contains no certificates")
		return
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		logger.Warning("Failed to parse panel certificate:", err)
		return
	}
	cert.Leaf = leaf

	m.mu.Lock()
	m.cert = &cert
	m.leaf = leaf
	m.certPath, m.keyPath = certPath, keyPath
	m.certMod, m.keyMod = certStat.ModTime(), keyStat.ModTime()
	m.mu.Unlock()

	if leaf.NotAfter.Before(time.Now()) {
		logger.Warningf("Panel certificate %q expired at %s; renew it to restore HTTPS enforcement",
			leaf.Subject.CommonName, leaf.NotAfter.Format(time.RFC3339))
	} else {
		logger.Infof("Panel certificate loaded: %q, expires %s", leaf.Subject.CommonName, leaf.NotAfter.Format(time.RFC3339))
	}
}

func (m *CertManager) HasCert() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cert != nil
}

func (m *CertManager) Valid() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.leaf == nil {
		return false
	}
	now := time.Now()
	return now.After(m.leaf.NotBefore) && now.Before(m.leaf.NotAfter)
}

func (m *CertManager) Leaf() *x509.Certificate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.leaf
}

func (m *CertManager) Paths() (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.certPath, m.keyPath
}

func (m *CertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	cert := m.cert
	domain := m.domain
	m.mu.RUnlock()

	if cert == nil {
		return nil, common.NewError("no panel certificate configured")
	}
	if domain != "" && !strings.EqualFold(hello.ServerName, domain) {
		return nil, common.NewErrorf("tls: unrecognized server name %q", hello.ServerName)
	}
	return cert, nil
}

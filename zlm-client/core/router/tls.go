package router

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"

	"github.com/gin-gonic/gin"
)

func adminPEMPath() string {
	return filepath.Join(config.LogDir(), "admin.pem")
}

func serveAdminCert(c *gin.Context) {
	raw, err := os.ReadFile(adminPEMPath())
	if err != nil {
		c.String(http.StatusNotFound, "no https cert yet")
		return
	}
	rest := raw
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type == "CERTIFICATE" {
			c.Header("Content-Disposition", `attachment; filename="zlm-admin.crt"`)
			c.Data(http.StatusOK, "application/x-x509-ca-cert", pem.EncodeToMemory(blk))
			return
		}
	}
	c.String(http.StatusNotFound, "pem has no certificate")
}

type quietTLSLog struct{}

func (quietTLSLog) Write(p []byte) (int, error) {
	s := strings.TrimSpace(string(p))
	if s == "" {
		return len(p), nil
	}
	if strings.Contains(s, "unknown certificate") ||
		strings.Contains(s, "certificate unknown") ||
		strings.Contains(s, "unknown certificate authority") {
		return len(p), nil
	}
	logger.Warnf("%s", s)
	return len(p), nil
}

func tlsErrorLog() *log.Logger {
	return log.New(quietTLSLog{}, "", 0)
}

func loadAdminTLS() *tls.Config {
	pemPath := adminPEMPath()
	if _, err := os.Stat(pemPath); err != nil {
		if err := writeSelfSigned(pemPath); err != nil {
			logger.Warnf("generate https cert failed: %v", err)
			for _, n := range config.C.Nodes {
				if n.Root == "" {
					continue
				}
				p := filepath.Join(n.Root, "default.pem")
				cert, e := tls.LoadX509KeyPair(p, p)
				if e != nil {
					continue
				}
				logger.Infor("https cert %s", p)
				return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			}
			return nil
		}
	}
	cert, err := tls.LoadX509KeyPair(pemPath, pemPath)
	if err != nil {
		logger.Warnf("load https cert failed: %v", err)
		return nil
	}
	logger.Infor("https cert %s", pemPath)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

func writeSelfSigned(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	hosts := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if h, _ := os.Hostname(); h != "" {
		hosts = append(hosts, h)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			ips = append(ips, ip)
		}
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"zlm-admin"}, CommonName: "zlm-admin"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return err
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
}

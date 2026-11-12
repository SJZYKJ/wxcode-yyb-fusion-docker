package protocol

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"

	"yyb_go/internal/store"
)

const (
	DeviceIdentityStable = "stable"
	DeviceIdentityRandom = "random"

	deviceSeedSetting = "protocol_device_seed"
)

// DeviceIdentity is the device an account presents to the WMPF endpoints.
// The official client keeps one persistent AppDeviceId and machine address;
// fresh random values on every ManualAuth and JSAPI request make one account
// look like a new device each time. An empty identity keeps that historical
// random behavior.
type DeviceIdentity struct {
	AppDeviceID []byte
	SessDevice  []byte
}

func (d DeviceIdentity) empty() bool {
	return len(d.AppDeviceID) == 0 || len(d.SessDevice) == 0
}

// deriveDeviceIdentity is deterministic for one instance seed and account, so
// restarts, cache rebuilds and re-scans of the same account keep one device.
// Different accounts and different installations never share a device.
func deriveDeviceIdentity(seed []byte, account string) DeviceIdentity {
	if len(seed) == 0 || account == "" {
		return DeviceIdentity{}
	}
	mac := deviceHMAC(seed, "yyb-go/sess-device/v1", account)[:6]
	return DeviceIdentity{
		AppDeviceID: deviceHMAC(seed, "yyb-go/app-device/v1", account),
		SessDevice:  formatSessDevice(mac),
	}
}

func deviceHMAC(seed []byte, label, account string) []byte {
	h := hmac.New(sha256.New, seed)
	h.Write([]byte(label))
	h.Write([]byte{0})
	h.Write([]byte(account))
	return h.Sum(nil)
}

// formatSessDevice renders a locally administered unicast MAC, the same shape
// as the historical random session device.
func formatSessDevice(mac []byte) []byte {
	b := append([]byte(nil), mac[:6]...)
	b[0] = (b[0] | 0x02) & 0xfe
	return []byte(fmt.Sprintf("%02X-%02X-%02X-%02X-%02X-%02X", b[0], b[1], b[2], b[3], b[4], b[5]))
}

func deviceAccountKey(acc *store.WechatAccount) string {
	if acc == nil {
		return ""
	}
	if acc.OpenID != "" {
		return acc.OpenID
	}
	if acc.ID > 0 {
		return "account:" + strconv.FormatInt(acc.ID, 10)
	}
	return ""
}

// deviceFor returns the stable device of acc, or an empty identity (random
// devices) when disabled or when the instance seed cannot be loaded.
func (p *Pool) deviceFor(ctx context.Context, acc *store.WechatAccount) DeviceIdentity {
	if p.cfg.DeviceIdentity == DeviceIdentityRandom {
		return DeviceIdentity{}
	}
	seed, err := p.deviceSeed(ctx)
	if err != nil {
		log.Printf("protocol: device identity seed unavailable, using random devices: %v", err)
		return DeviceIdentity{}
	}
	return deriveDeviceIdentity(seed, deviceAccountKey(acc))
}

func (p *Pool) deviceSeed(ctx context.Context) ([]byte, error) {
	p.deviceSeedMu.Lock()
	defer p.deviceSeedMu.Unlock()
	if len(p.deviceSeedValue) != 0 {
		return p.deviceSeedValue, nil
	}
	if p.db == nil {
		return nil, fmt.Errorf("no database")
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	// Concurrent instances on one database converge on the first stored seed.
	value, err := p.db.EnsureSetting(ctx, deviceSeedSetting, hex.EncodeToString(fresh))
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(value)
	if err != nil || len(seed) < 16 {
		return nil, fmt.Errorf("invalid stored device seed")
	}
	p.deviceSeedValue = seed
	return seed, nil
}

package protocol

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"yyb_go/internal/store"
)

var sessDevicePattern = regexp.MustCompile(`^[0-9A-F]{2}(-[0-9A-F]{2}){5}$`)

func assertSessDeviceShape(t *testing.T, device []byte) {
	t.Helper()
	if !sessDevicePattern.Match(device) {
		t.Fatalf("session device %q is not a MAC address", device)
	}
	first, err := strconv.ParseUint(string(device[:2]), 16, 8)
	if err != nil {
		t.Fatal(err)
	}
	if first&0x02 == 0 || first&0x01 != 0 {
		t.Fatalf("session device %q is not a locally administered unicast MAC", device)
	}
}

func TestDeriveDeviceIdentityIsStablePerAccount(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, 32)
	a := deriveDeviceIdentity(seed, "openid-a")
	again := deriveDeviceIdentity(seed, "openid-a")
	b := deriveDeviceIdentity(seed, "openid-b")
	otherInstance := deriveDeviceIdentity(bytes.Repeat([]byte{8}, 32), "openid-a")

	if len(a.AppDeviceID) != 32 {
		t.Fatalf("AppDeviceID length = %d, want 32", len(a.AppDeviceID))
	}
	assertSessDeviceShape(t, a.SessDevice)
	if !bytes.Equal(a.AppDeviceID, again.AppDeviceID) || !bytes.Equal(a.SessDevice, again.SessDevice) {
		t.Fatal("one account must keep the same device")
	}
	for name, other := range map[string]DeviceIdentity{"other account": b, "other instance": otherInstance} {
		if bytes.Equal(a.AppDeviceID, other.AppDeviceID) || bytes.Equal(a.SessDevice, other.SessDevice) {
			t.Fatalf("%s shares the device", name)
		}
	}
}

func TestDeriveDeviceIdentityRequiresSeedAndAccount(t *testing.T) {
	if !deriveDeviceIdentity(nil, "openid").empty() || !deriveDeviceIdentity([]byte("seed"), "").empty() {
		t.Fatal("missing seed or account must keep random devices")
	}
}

func TestPoolDeviceSurvivesRestartAndCanBeDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "yyb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acc := &store.WechatAccount{ID: 3, OpenID: "openid-restart"}

	first := NewPool(DefaultConfig(), db).deviceFor(ctx, acc)
	restarted := NewPool(DefaultConfig(), db).deviceFor(ctx, acc)
	if first.empty() || !bytes.Equal(first.AppDeviceID, restarted.AppDeviceID) || !bytes.Equal(first.SessDevice, restarted.SessDevice) {
		t.Fatal("device must survive a restart on the same database")
	}
	seed, err := db.GetSetting(ctx, deviceSeedSetting)
	if err != nil || len(seed) != 64 {
		t.Fatalf("stored seed = %q, %v", seed, err)
	}
	// Re-scan keeps the account's OpenID, and therefore its device. Without an
	// OpenID the row identity is used.
	byID := NewPool(DefaultConfig(), db).deviceFor(ctx, &store.WechatAccount{ID: 3})
	if byID.empty() || bytes.Equal(byID.AppDeviceID, first.AppDeviceID) {
		t.Fatal("account without OpenID must still get its own device")
	}

	cfg := DefaultConfig()
	cfg.DeviceIdentity = DeviceIdentityRandom
	if !NewPool(cfg, db).deviceFor(ctx, acc).empty() {
		t.Fatal("random mode must keep the historical random devices")
	}
}

func TestStateAttachesAccountDeviceToCachedSession(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "yyb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	acc, err := db.UpsertAccount(ctx, "synthetic-device-account", "", nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := WmpfSession{
		Session: AppSession{UIN: 1234, SendKey: []byte("synthetic-send"), RecvKey: []byte("synthetic-recv")},
		PSK:     pskEntry{PSKType: 1, PreSharedKey: "001122", TicketEntry: "aabbcc", ExpiredAt: time.Now().Add(time.Hour).Unix()},
	}
	if err := db.PutSession(ctx, acc.ID, &st.Session.UIN, st.toBlob(), time.Now().Add(time.Hour).Unix(), ""); err != nil {
		t.Fatal(err)
	}

	pool := NewPool(DefaultConfig(), db)
	got, err := pool.state(ctx, "", acc.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	want := pool.deviceFor(ctx, acc)
	if want.empty() || !bytes.Equal(got.Device.SessDevice, want.SessDevice) || !bytes.Equal(got.Device.AppDeviceID, want.AppDeviceID) {
		t.Fatal("cached session does not carry the account device")
	}
	if _, ok := st.toBlob()["device"]; ok {
		t.Fatal("derived device must not be persisted in the session blob")
	}

	cfg := DefaultConfig()
	cfg.DeviceIdentity = DeviceIdentityRandom
	random, err := NewPool(cfg, db).state(ctx, "", acc.ID, "", false)
	if err != nil || !random.Device.empty() {
		t.Fatalf("random mode session device = %+v, err = %v", random.Device, err)
	}
}

func TestJSAPIRequestsUseAccountSessionDevice(t *testing.T) {
	device := []byte("06-11-22-33-44-55")
	host := hostAppIDDefault
	operate, err := buildOperateRequest(1234, "wx0000000000000001", map[string]any{"api_name": "webapi_getuserinfo"}, host, device)
	if err != nil {
		t.Fatal(err)
	}
	for name, plain := range map[string][]byte{
		"js-login": buildJSAPIPlaintext(1234, "wx0000000000000001", jsLoginURL, jsLoginCmdID, nil, host, device),
		"phone":    buildPhoneRequest(1234, "wx0000000000000001", host, device),
		"operate":  operate,
	} {
		// Outer session info and the inner request both carry the device.
		if got := bytes.Count(plain, device); got != 2 {
			t.Fatalf("%s carries the account device %d times, want 2", name, got)
		}
	}

	first := buildPhoneRequest(1234, "wx0000000000000001", host, nil)
	second := buildPhoneRequest(1234, "wx0000000000000001", host, nil)
	if bytes.Equal(first, second) {
		t.Fatal("without an account device each request keeps a random device")
	}
}

func TestManualAuthCarriesProvidedAppDeviceID(t *testing.T) {
	buffer := base64.StdEncoding.EncodeToString(append(pbLen(1, []byte("ticket")), pbLen(2, []byte("server-device"))...))
	device := deriveDeviceIdentity(bytes.Repeat([]byte{9}, 32), "openid-login")
	request, err := buildManualAuthRequest(buffer, device.AppDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	appBase, ok := pbParse(request)[1].([]byte)
	if !ok {
		t.Fatal("ManualAuthRequest has no AppBaseRequest")
	}
	if got, _ := pbParse(appBase)[1].([]byte); !bytes.Equal(got, device.AppDeviceID) {
		t.Fatalf("AppBaseRequest device = %x, want the account device", got)
	}
}

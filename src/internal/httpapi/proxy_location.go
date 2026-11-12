package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var (
	publicIPLocationEndpoint = "http://ip-api.com/json/"
	reverseGeocodeEndpoint   = "https://nominatim.openstreetmap.org/reverse"
)

type proxyLocationIn struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

type proxyLocation struct {
	Province     string `json:"province"`
	City         string `json:"city"`
	Source       string `json:"source"`
	IP           string `json:"ip,omitempty"`
	ProvinceCode string `json:"province_code,omitempty"`
	CityCode     string `json:"city_code,omitempty"`
	Matched      bool   `json:"matched"`
}

func (a *App) handleProxyLocationRecommend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body proxyLocationIn
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	location, err := a.lookupProxyLocation(r.Context(), r, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法推荐代理地区: "+err.Error())
		return
	}
	if err := a.matchProxyLocation(r.Context(), &location); err != nil {
		writeError(w, http.StatusBadGateway, "地区已定位，但无法匹配代理地区: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, location)
}

func (a *App) lookupProxyLocation(ctx context.Context, r *http.Request, body proxyLocationIn) (proxyLocation, error) {
	if body.Latitude != nil || body.Longitude != nil {
		if body.Latitude == nil || body.Longitude == nil || *body.Latitude < -90 || *body.Latitude > 90 || *body.Longitude < -180 || *body.Longitude > 180 {
			return proxyLocation{}, fmt.Errorf("手机定位坐标无效")
		}
		return a.reverseGeocode(ctx, *body.Latitude, *body.Longitude)
	}
	ip := net.ParseIP(strings.TrimSpace(a.clientIP(r)))
	lookupIP := ""
	source := "server_public_ip"
	if ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() {
		lookupIP = ip.String()
		source = "client_public_ip"
	}
	return a.lookupPublicIP(ctx, lookupIP, source)
}

func (a *App) lookupPublicIP(ctx context.Context, ip, source string) (proxyLocation, error) {
	endpoint := publicIPLocationEndpoint
	if ip != "" {
		endpoint += url.PathEscape(ip)
	}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	endpoint += separator + "lang=zh-CN&fields=status,message,country,regionName,city,query"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return proxyLocation{}, err
	}
	req.Header.Set("User-Agent", "wxcode-YYB location recommendation")
	var payload struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Province string `json:"regionName"`
		City     string `json:"city"`
		IP       string `json:"query"`
	}
	if err := a.getLocationJSON(ctx, req, &payload); err != nil {
		return proxyLocation{}, err
	}
	if payload.Status != "success" || strings.TrimSpace(payload.Province) == "" {
		if payload.Message == "" {
			payload.Message = "公网 IP 未返回省市"
		}
		return proxyLocation{}, fmt.Errorf("%s", payload.Message)
	}
	return proxyLocation{Province: payload.Province, City: payload.City, IP: payload.IP, Source: source}, nil
}

func (a *App) reverseGeocode(ctx context.Context, latitude, longitude float64) (proxyLocation, error) {
	endpoint, err := url.Parse(reverseGeocodeEndpoint)
	if err != nil {
		return proxyLocation{}, err
	}
	query := endpoint.Query()
	query.Set("format", "jsonv2")
	query.Set("addressdetails", "1")
	query.Set("accept-language", "zh-CN")
	query.Set("lat", strconv.FormatFloat(latitude, 'f', 6, 64))
	query.Set("lon", strconv.FormatFloat(longitude, 'f', 6, 64))
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return proxyLocation{}, err
	}
	req.Header.Set("User-Agent", "wxcode-YYB/1.0 location recommendation")
	var payload struct {
		Address struct {
			State        string `json:"state"`
			Province     string `json:"province"`
			City         string `json:"city"`
			Municipality string `json:"municipality"`
			Town         string `json:"town"`
			County       string `json:"county"`
		} `json:"address"`
	}
	if err := a.getLocationJSON(ctx, req, &payload); err != nil {
		return proxyLocation{}, err
	}
	province := firstLocationValue(payload.Address.State, payload.Address.Province, payload.Address.Municipality)
	city := firstLocationValue(payload.Address.City, payload.Address.Municipality, payload.Address.Town, payload.Address.County)
	if province == "" {
		return proxyLocation{}, fmt.Errorf("手机定位未返回省份")
	}
	return proxyLocation{Province: province, City: city, Source: "browser_geolocation"}, nil
}

func (a *App) getLocationJSON(ctx context.Context, req *http.Request, dst any) error {
	client := &http.Client{Timeout: a.cfg.RequestTimeout}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("定位服务返回 HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(dst); err != nil {
		return fmt.Errorf("无法解析定位服务响应")
	}
	return nil
}

func (a *App) matchProxyLocation(ctx context.Context, location *proxyLocation) error {
	provinces, err := a.fetchProxyAreas(ctx, ipzanProvinceAreasEndpoint)
	if err != nil {
		return err
	}
	province, ok := matchProxyArea(provinces, location.Province)
	if !ok {
		return fmt.Errorf("代理地区列表中没有 %s", location.Province)
	}
	location.Province = province.Name
	location.ProvinceCode = province.Code
	location.Matched = true
	if strings.TrimSpace(location.City) == "" {
		return nil
	}
	cities, err := a.fetchProxyAreas(ctx, ipzanCityAreasEndpoint+url.QueryEscape(province.Code))
	if err != nil {
		return err
	}
	if city, matched := matchProxyArea(cities, location.City); matched {
		location.City = city.Name
		location.CityCode = city.Code
	} else {
		location.City = ""
	}
	return nil
}

func matchProxyArea(areas []proxyArea, name string) (proxyArea, bool) {
	want := normalizeRegionName(name)
	for _, area := range areas {
		if normalizeRegionName(area.Name) == want {
			return area, true
		}
	}
	return proxyArea{}, false
}

func firstLocationValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

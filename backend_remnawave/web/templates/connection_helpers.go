package templates

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/shdvr/vpn-backend/internal/remnawave"
)

// loc picks the user's locale from a Remnawave LocalizedText, falling back
// to en, ru, or first key. Empty if the map is empty.
func loc(t remnawave.LocalizedText, lang string) string {
	if t == nil {
		return ""
	}
	if v, ok := t[lang]; ok && v != "" {
		return v
	}
	for _, k := range []string{"en", "ru"} {
		if v, ok := t[k]; ok && v != "" {
			return v
		}
	}
	for _, v := range t {
		if v != "" {
			return v
		}
	}
	return ""
}

// baseTr resolves an admin-supplied base translation (e.g.
// installationGuideHeader), falling back to the provided default if missing.
func baseTr(d ConnectionData, key, fallback string) string {
	if d.BaseTranslations == nil {
		return fallback
	}
	if t, ok := d.BaseTranslations[key]; ok {
		if v := loc(t, d.Lang); v != "" {
			return v
		}
	}
	return fallback
}

// svgRaw returns the panel-stored SVG markup. Admin-controlled input, served
// inside our trusted admin perimeter — we render it as raw HTML.
func svgRaw(lib map[string]remnawave.SvgEntry, key string) string {
	if key == "" || lib == nil {
		return ""
	}
	if e, ok := lib[key]; ok {
		return e.SvgString
	}
	return ""
}

func itoaSafe(i int) string { return strconv.Itoa(i) }

func isCopyButton(b remnawave.Button) bool {
	return strings.EqualFold(b.Type, "copyButton")
}

// buttonHref applies the {{SUBSCRIPTION_LINK}} template (the only one we
// support; happ crypt links omitted in this MVP) and returns a safe-looking
// URL, or empty if no usable href. For subscriptionLink buttons with no URL,
// fall back to the app's deep link (urlScheme + subscriptionURL).
func buttonHref(b remnawave.Button, subscriptionURL string) string {
	raw := b.URL
	if raw == "" {
		raw = b.Link
	}
	raw = strings.ReplaceAll(raw, "{{SUBSCRIPTION_LINK}}", subscriptionURL)
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return ""
	}
	for _, bad := range []string{"javascript:", "data:", "vbscript:", "file:"} {
		if strings.HasPrefix(lower, bad) {
			return ""
		}
	}
	return raw
}

// PlatformDisplayName returns the admin-localized platform name, or "".
func PlatformDisplayName(p remnawave.Platform, lang string) string {
	return loc(p.DisplayName, lang)
}

// BuildDeepLink applies app.urlScheme to the subscription URL, honoring the
// isNeedBase64Encoding flag. Used by the handler to pre-resolve a buttonless
// subscriptionLink button into a clickable URL.
func BuildDeepLink(app remnawave.App, subscriptionURL string) string {
	if app.UrlScheme == "" || subscriptionURL == "" {
		return ""
	}
	payload := subscriptionURL
	if app.IsNeedBase64Encoding {
		payload = base64.StdEncoding.EncodeToString([]byte(payload))
	}
	return app.UrlScheme + payload
}

// buttonLabel returns the localized label or the fallback if missing.
func buttonLabel(d ConnectionData, b remnawave.Button, fallback string) string {
	if v := loc(b.Text, d.Lang); v != "" {
		return v
	}
	return fallback
}

// targetFor opens external (http/https) links in a new tab; deep links stay.
func targetFor(buttonType string) string {
	if strings.EqualFold(buttonType, "external") {
		return "_blank"
	}
	return ""
}

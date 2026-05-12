// balancer-gen renders an xray client config with multi-tier least-load
// balancing + burstObservatory ping probing. Each tier maps to a group of
// Remnawave node names; balancer-gen resolves names to UUIDs via the panel
// API and emits the Remnawave-fork `injectHosts` blocks (one per tier) so
// the subscription-server expands them per-user at fetch time.
//
// Usage:
//
//	REMNAWAVE_BASE_URL=https://panel... REMNAWAVE_TOKEN=... \
//	  go run ./cmd/balancer-gen -tiers tiers.yaml > xray.json
//
// tiers.yaml shape:
//
//	tier1:        # tightest RTT, primary
//	  - nl-1
//	  - de-1
//	tier2:        # fallback if tier1 dies
//	  - fi-1
//	tier3:
//	  - us-1
//
// Tier order = priority (tier1 → tier2 → tier3 → direct fallback).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/shdvr/vpn-backend/internal/remnawave"
)

func main() {
	tiersPath := flag.String("tiers", "tiers.yaml", "path to tier mapping")
	flag.Parse()

	baseURL := mustEnv("REMNAWAVE_BASE_URL")
	token := mustEnv("REMNAWAVE_TOKEN")

	tiers, tierOrder, err := loadTiers(*tiersPath)
	if err != nil {
		die("load tiers: %v", err)
	}

	rw := remnawave.New(baseURL, token)
	nodes, err := rw.ListNodes(context.Background())
	if err != nil {
		die("list nodes: %v", err)
	}
	byName := map[string]uuid.UUID{}
	for _, n := range nodes {
		byName[n.Name] = n.UUID
	}

	out := buildConfig(tiers, tierOrder, byName)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		die("encode: %v", err)
	}
}

func loadTiers(path string) (map[string][]string, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	// yaml.Node preserves declaration order — we need it for tier priority.
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil, fmt.Errorf("empty yaml")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("yaml root not a map")
	}
	tiers := map[string][]string{}
	var order []string
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i].Value
		var names []string
		if err := root.Content[i+1].Decode(&names); err != nil {
			return nil, nil, fmt.Errorf("tier %q: %w", key, err)
		}
		tiers[key] = names
		order = append(order, key)
	}
	return tiers, order, nil
}

// --- xray config types (only fields we emit) ---

type Config struct {
	DNS              map[string]any   `json:"dns"`
	Log              map[string]any   `json:"log"`
	Routing          Routing          `json:"routing"`
	Inbounds         []map[string]any `json:"inbounds"`
	Outbounds        []map[string]any `json:"outbounds"`
	Remnawave        Remnawave        `json:"remnawave"`
	BurstObservatory BurstObservatory `json:"burstObservatory"`
}

type Routing struct {
	Rules          []map[string]any `json:"rules"`
	Balancers      []Balancer       `json:"balancers"`
	DomainMatcher  string           `json:"domainMatcher"`
	DomainStrategy string           `json:"domainStrategy"`
}

type Balancer struct {
	Tag         string   `json:"tag"`
	Selector    []string `json:"selector"`
	Strategy    Strategy `json:"strategy"`
	FallbackTag string   `json:"fallbackTag"`
}

type Strategy struct {
	Type     string          `json:"type"`
	Settings StrategySetting `json:"settings"`
}

type StrategySetting struct {
	Costs     []Cost   `json:"costs"`
	MaxRTT    string   `json:"maxRTT"`
	Expected  int      `json:"expected"`
	Baselines []string `json:"baselines"`
	Tolerance float64  `json:"tolerance"`
}

type Cost struct {
	Match string `json:"match"`
	Value int    `json:"value"`
}

type Remnawave struct {
	InjectHosts []InjectHost `json:"injectHosts"`
}

type InjectHost struct {
	Selector   InjectSelector `json:"selector"`
	TagPrefix  string         `json:"tagPrefix"`
	SelectFrom string         `json:"selectFrom"`
}

type InjectSelector struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type BurstObservatory struct {
	PingConfig      PingConfig `json:"pingConfig"`
	SubjectSelector []string   `json:"subjectSelector"`
}

type PingConfig struct {
	Timeout     string `json:"timeout"`
	Interval    string `json:"interval"`
	Sampling    int    `json:"sampling"`
	Destination string `json:"destination"`
}

// --- tier knobs: tighter RTT/baseline for higher tiers ---

type tierKnobs struct {
	MaxRTT, Baseline string
	Tolerance        float64
}

var defaultKnobs = []tierKnobs{
	{"4000ms", "1000ms", 0.35},
	{"5500ms", "1500ms", 0.40},
	{"8000ms", "2000ms", 0.45},
	{"12000ms", "3000ms", 0.50},
}

func knobsFor(i int) tierKnobs {
	if i < len(defaultKnobs) {
		return defaultKnobs[i]
	}
	return defaultKnobs[len(defaultKnobs)-1]
}

func buildConfig(tiers map[string][]string, order []string, byName map[string]uuid.UUID) Config {
	injects := make([]InjectHost, 0, len(order))
	balancers := make([]Balancer, 0, len(order))

	for i, tier := range order {
		names := tiers[tier]
		uuids := make([]string, 0, len(names))
		for _, n := range names {
			u, ok := byName[n]
			if !ok {
				die("node %q from tier %q not found in Remnawave panel", n, tier)
			}
			uuids = append(uuids, u.String())
		}
		sort.Strings(uuids)

		// One injectHosts block per tier. tagPrefix = tier name → expanded
		// tags: <tier>, <tier>-2, <tier>-3, ...
		injects = append(injects, InjectHost{
			Selector:   InjectSelector{Type: "uuids", Values: uuids},
			TagPrefix:  tier,
			SelectFrom: "ALL",
		})

		// Balancer selector uses prefix → matches all tags starting with tier.
		// xray balancer.selector is a substring/prefix match list.
		selector := []string{tier}
		costs := []Cost{{Match: tier, Value: 1}}

		fallback := "direct"
		if i+1 < len(order) {
			fallback = order[i+1]
		}
		k := knobsFor(i)
		balancers = append(balancers, Balancer{
			Tag:      tier,
			Selector: selector,
			Strategy: Strategy{
				Type: "leastLoad",
				Settings: StrategySetting{
					Costs:     costs,
					MaxRTT:    k.MaxRTT,
					Expected:  max(1, len(uuids)/2),
					Baselines: []string{k.Baseline},
					Tolerance: k.Tolerance,
				},
			},
			FallbackTag: fallback,
		})
	}

	primary := order[0]

	return Config{
		DNS: map[string]any{
			"servers":       []string{"77.88.8.8", "1.1.1.1", "1.0.0.1"},
			"queryStrategy": "UseIP",
		},
		Log: map[string]any{"loglevel": "warning"},
		Routing: Routing{
			Rules: []map[string]any{
				{"port": 53, "type": "field", "outboundTag": primary},
				{
					"ip": []string{
						"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
						"127.0.0.0/8", "169.254.0.0/16",
						"224.0.0.0/4", "240.0.0.0/4",
						"::1/128", "fc00::/7", "fe80::/10",
					},
					"type":        "field",
					"outboundTag": "direct",
				},
				{"type": "field", "protocol": []string{"bittorrent"}, "outboundTag": "direct"},
				{"type": "field", "network": "tcp,udp", "balancerTag": primary},
			},
			Balancers:      balancers,
			DomainMatcher:  "hybrid",
			DomainStrategy: "IPIfNonMatch",
		},
		Inbounds: []map[string]any{
			{
				"tag": "socks", "port": 10808, "listen": "127.0.0.1", "protocol": "socks",
				"settings": map[string]any{"udp": true, "auth": "noauth"},
				"sniffing": map[string]any{"enabled": true, "routeOnly": true, "destOverride": []string{"http", "tls", "quic"}},
			},
			{
				"tag": "http", "port": 10809, "listen": "127.0.0.1", "protocol": "http",
				"settings": map[string]any{"allowTransparent": false},
				"sniffing": map[string]any{"enabled": true, "routeOnly": true, "destOverride": []string{"http", "tls", "quic"}},
			},
		},
		Outbounds: []map[string]any{
			{"tag": "direct", "protocol": "freedom"},
			{"tag": "block", "protocol": "blackhole"},
		},
		Remnawave: Remnawave{InjectHosts: injects},
		BurstObservatory: BurstObservatory{
			PingConfig: PingConfig{
				Timeout:     "12s",
				Interval:    "75s",
				Sampling:    5,
				Destination: "http://www.gstatic.com/generate_204",
			},
			SubjectSelector: order, // probe all tier prefixes
		},
	}
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		die("env %s required", k)
	}
	return v
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "balancer-gen: "+f+"\n", a...)
	os.Exit(1)
}

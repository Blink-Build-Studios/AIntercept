package chart

import (
	"os"
	"strings"
	"testing"
)

// Envoy's SDS stream to certsvc sits idle for hours between renewals. With no
// keepalive, a connection the network dropped silently never got the next
// push, and one Envoy pod served an expired leaf (2026-11-15). Every Envoy
// config must ping certsvc so a dead stream is re-opened.
func TestEnvoyPingsCertsvc(t *testing.T) {
	for _, f := range []string{
		chartDir + "/files/envoy.m2.yaml",
		chartDir + "/files/envoy.multi.yaml",
		"../../../envoy/envoy.yaml",
		"../../../envoy/envoy.m2.yaml",
		"../../../envoy/envoy.multi.yaml",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		_, rest, ok := strings.Cut(string(b), "- name: certsvc\n")
		if !ok {
			t.Fatalf("%s: no certsvc cluster", f)
		}
		cluster, _, _ := strings.Cut(rest, "load_assignment:")
		for _, want := range []string{"connection_keepalive:", "tcp_keepalive:"} {
			if !strings.Contains(cluster, want) {
				t.Errorf("%s: the certsvc cluster has no %s", f, want)
			}
		}
	}
}

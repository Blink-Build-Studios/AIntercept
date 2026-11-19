package chart

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The chart deploys its own release's images by default: the ones the release
// workflow publishes to ghcr.io, tagged v<appVersion>. A set tag or digest
// still wins.
func TestImagesDefaultToTheRelease(t *testing.T) {
	chart, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^appVersion: "?([^"\n]+)"?$`).FindSubmatch(chart)
	if m == nil {
		t.Fatal("Chart.yaml has no appVersion")
	}
	v := "v" + string(m[1])
	image := func(docs []map[string]any, name, container string) any {
		return byName(get(find(docs, "Deployment", name), "spec", "template", "spec", "containers"))[container]["image"]
	}

	docs := render(t, "api.enabled=true")
	for _, c := range []struct{ deploy, container, want string }{
		{"envoy", "observer", "ghcr.io/blink-build-studios/aintercept:" + v},
		{"worker", "worker", "ghcr.io/blink-build-studios/aintercept:" + v},
		{"certsvc", "certsvc", "ghcr.io/blink-build-studios/aintercept:" + v},
		{"api", "api", "ghcr.io/blink-build-studios/aintercept-api:" + v},
	} {
		if got := image(docs, c.deploy, c.container); got != c.want {
			t.Errorf("%s/%s image = %v, want %s", c.deploy, c.container, got, c.want)
		}
	}

	docs = render(t, "api.enabled=true", "images.aintercept.tag=custom", "images.api.digest=sha256:abc")
	if got := image(docs, "worker", "worker"); got != "ghcr.io/blink-build-studios/aintercept:custom" {
		t.Errorf("a set tag: worker image = %v", got)
	}
	if got := image(docs, "api", "api"); got != "ghcr.io/blink-build-studios/aintercept-api@sha256:abc" {
		t.Errorf("a set digest: api image = %v", got)
	}
}

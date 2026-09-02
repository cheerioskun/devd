package storage

import "testing"

func TestQualifyImage(t *testing.T) {
	tests := map[string]string{
		"alpine":                          "docker.io/library/alpine",
		"nicolaka/netshoot":               "docker.io/nicolaka/netshoot",
		"ghcr.io/example/image:latest":    "ghcr.io/example/image:latest",
		"localhost:5000/example/image:v1": "localhost:5000/example/image:v1",
		"oci-archive:/tmp/image.tar":      "oci-archive:/tmp/image.tar",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := QualifyImage(input); got != want {
				t.Fatalf("QualifyImage(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

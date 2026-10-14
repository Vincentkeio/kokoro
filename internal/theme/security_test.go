package theme

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
)

func TestResourceReferencesCannotEscapeThemeAssets(t *testing.T) {
	for _, value := range []string{
		"url(https://evil.example/_theme-assets/bg.png)",
		"url(//evil.example/_theme-assets/bg.png)",
		"url(/_theme-assets/bg.png), url(https://evil.example/x)",
		"url(/_theme-assets/../../admin)",
		"url(/_theme-assets/%2e%2e/admin)",
		"url(data:image/svg+xml,%3Csvg%20onload=alert%281%29%3E)",
		"linear-gradient(red, blue), image-set('https://evil.example/x' 1x)",
		"url(/_theme-assets/bg.png)\x00",
	} {
		if err := ValidateTokens(map[string]string{"--kokoro-bg-image": value}, "tokens"); err == nil {
			t.Errorf("accepted external or unsafe image: %q", value)
		}
	}
	for _, value := range []string{"url(/_theme-assets/bg.webp)", `url("/_theme-assets/bg.webp")`, "url('/_theme-assets/acme.theme/bg.png')", "url(data:image/png,abc)"} {
		if err := ValidateTokens(map[string]string{"--kokoro-bg-image": value}, "tokens"); err != nil {
			t.Errorf("rejected local image %q: %v", value, err)
		}
	}
}

func TestLayoutLengthsCannotInjectCSSOrHTML(t *testing.T) {
	for _, value := range []string{
		"calc(1px + </style><script>alert(1)</script>)",
		"calc(1px + var(--x, red; color:red))",
		"calc(1px + url(https://evil.example/_theme-assets/x))",
	} {
		l := Layout{}
		l.Charts.Height = value
		if ValidateLayout(&l) == nil {
			t.Errorf("accepted injected layout value: %q", value)
		}
	}
	for _, value := range []string{"240px", "clamp(200px, 25vh, 400px)", "var(--kokoro-chart-height)"} {
		l := Layout{}
		l.Charts.Height = value
		if err := ValidateLayout(&l); err != nil {
			t.Errorf("rejected legitimate layout: %v", err)
		}
	}
}

func TestPackageDuplicateEntriesAndReorderedChecksum(t *testing.T) {
	m := &Manifest{ID: "acme.secure", Name: "Secure", Version: "1.0.0", Tokens: map[string]string{"--kokoro-color-primary": "#123456"}}
	raw, err := BuildPackage(m, map[string][]byte{"a.png": []byte("a"), "z.png": []byte("z")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	original, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	rebuild := func(duplicate string) []byte {
		var out bytes.Buffer
		zw := zip.NewWriter(&out)
		for i := len(original.File) - 1; i >= 0; i-- {
			f := original.File[i]
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
			w, err := zw.Create(f.Name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(body); err != nil {
				t.Fatal(err)
			}
			if f.Name == duplicate {
				w, err = zw.Create(f.Name)
				if err != nil {
					t.Fatal(err)
				}
				w.Write(body)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	if _, err := ExtractPackage(rebuild("")); err != nil {
		t.Fatalf("valid reordered package rejected: %v", err)
	}
	for _, name := range []string{"theme.json", "assets/a.png"} {
		if _, err := ExtractPackage(rebuild(name)); err == nil {
			t.Errorf("duplicate %s accepted", name)
		}
	}
}

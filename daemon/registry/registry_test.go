package registry

import (
	"reflect"
	"strings"
	"testing"
)

var (
	appA = App{Server: "https://a.example", Token: "ta"}
	appB = App{Server: "https://b.example", Token: "tb"}
)

func ptr(s string) *string { return &s }

func wantEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseReadsAppsAndBrowserGrants(t *testing.T) {
	text := `{"apps":[{"server":"https://a.example","token":"ta"},{"server":"https://b.example","token":"tb"}],` +
		`"browsers":[{"origin":"https://c.example","grantedAt":"2026-10-19T00:00:00.000Z"}]}`
	got, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, got, Registry{Apps: []App{appA, appB}, Browsers: []BrowserGrant{{Origin: "https://c.example", GrantedAt: "2026-10-19T00:00:00.000Z"}}})
}

func TestParseMissingBrowsersIsEmpty(t *testing.T) {
	for _, text := range []string{`{"apps":[{"server":"https://a.example","token":"ta"}]}`, `{"apps":[{"server":"https://a.example","token":"ta"}],"browsers":null}`} {
		got, err := Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		wantEqual(t, got, Registry{Apps: []App{appA}, Browsers: []BrowserGrant{}})
	}
}

func TestParseNamesBrokenJSONAndIncompleteFiles(t *testing.T) {
	tests := []struct{ name, text, want string }{
		{"broken JSON", "{oops", "not valid JSON"},
		{"empty file", "", "not valid JSON"},
		{"no apps list", `{"server":"https://s","token":"t"}`, "has no apps list"},
		{"empty token", `{"apps":[{"server":"https://s","token":""}]}`, "has no apps list"},
		{"no token", `{"apps":[{"server":"https://s"}]}`, "has no apps list"},
		{"null app", `{"apps":[null]}`, "has no apps list"},
		{"null document", `null`, "has no apps list"},
		{"array document", `[]`, "has no apps list"},
		{"browsers not a list", `{"apps":[],"browsers":{}}`, "has no apps list"},
		{"grant without time", `{"apps":[],"browsers":[{"origin":"https://c"}]}`, "has no apps list"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse(%q) error = %v, want it to contain %q", tc.text, err, tc.want)
			}
		})
	}
}

func TestParseDropsUnknownFields(t *testing.T) {
	got, err := Parse(`{"apps":[{"server":"https://a.example","token":"ta","extra":1}],"browsers":[],"x":true}`)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, got, Registry{Apps: []App{appA}, Browsers: []BrowserGrant{}})
}

// The file of a Node install (JSON.stringify(registry, null, 2) and a newline) is what Marshal writes.
func TestMarshalIsTheBytesTheNodeDaemonWrote(t *testing.T) {
	r := Empty().AddApp(App{Server: "https://a.example", Token: "t<&>"}).AddBrowser("https://c.example", "2026-10-19T00:00:00.000Z")
	want := `{
  "apps": [
    {
      "server": "https://a.example",
      "token": "t<&>"
    }
  ],
  "browsers": [
    {
      "origin": "https://c.example",
      "grantedAt": "2026-10-19T00:00:00.000Z"
    }
  ]
}
`
	if got := string(r.Marshal()); got != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", got, want)
	}
	if got, want := string(Empty().Marshal()), "{\n  \"apps\": [],\n  \"browsers\": []\n}\n"; got != want {
		t.Errorf("Marshal(empty) = %q, want %q", got, want)
	}
	if got := string(Registry{}.Marshal()); got != string(Empty().Marshal()) {
		t.Errorf("Marshal(zero value) = %q, want the empty registry", got)
	}
}

func TestMarshalParseRoundTrip(t *testing.T) {
	r := Empty().AddApp(appA).AddApp(appB).AddBrowser("https://c.example", "t")
	got, err := Parse(string(r.Marshal()))
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, got, r)
}

func TestAddAppTwiceForOneServerKeepsOneEntryWithTheLaterToken(t *testing.T) {
	r := Empty().AddApp(appA).AddApp(App{Server: appA.Server, Token: "new"})
	wantEqual(t, r.Apps, []App{{Server: appA.Server, Token: "new"}})
}

func TestAddAppKeepsTheOrderOfFirstPairing(t *testing.T) {
	wantEqual(t, Empty().AddApp(appA).AddApp(appB).AddApp(appA).Apps, []App{appA, appB})
}

func TestEditsDoNotChangeTheirInput(t *testing.T) {
	r := Empty().AddApp(appA)
	_ = r.AddApp(App{Server: appA.Server, Token: "new"})
	_ = r.RemoveOrigin(appA.Server)
	wantEqual(t, r.Apps, []App{appA})
}

func TestRemoveOriginDropsTheAppAndTheBrowserGrantOfThatOriginOnly(t *testing.T) {
	r := Empty().AddApp(appA).AddApp(appB).AddBrowser(appA.Server, "t0").AddBrowser("https://other.example", "t1")
	r = r.RemoveOrigin(appA.Server)
	wantEqual(t, r.Apps, []App{appB})
	wantEqual(t, r.Browsers, []BrowserGrant{{Origin: "https://other.example", GrantedAt: "t1"}})
}

func TestHasAppAndHasBrowserAnswerByOrigin(t *testing.T) {
	r := Empty().AddApp(appA).AddBrowser("https://c.example", "t")
	for _, tc := range []struct {
		name string
		got  bool
		want bool
	}{
		{"app a", r.HasApp(appA.Server), true},
		{"app b", r.HasApp(appB.Server), false},
		{"browser c", r.HasBrowser("https://c.example"), true},
		{"browser a", r.HasBrowser(appA.Server), false},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestWithdrawnIsWhatRunsForAnOriginTheRegistryNoLongerHas(t *testing.T) {
	r := Empty().AddApp(appA).AddBrowser("https://c.example", "t")
	wantEqual(t, r.Withdrawn([]string{appA.Server, appB.Server}, []string{"https://c.example", "https://d.example"}),
		Ended{Apps: []string{appB.Server}, Pages: []string{"https://d.example"}})
	wantEqual(t, r.Withdrawn([]string{appA.Server}, []string{"https://c.example"}), Ended{Apps: []string{}, Pages: []string{}})
}

func TestARepeatedBrowserGrantIsStoredOnce(t *testing.T) {
	r := Empty().AddBrowser("https://c.example", "t0").AddBrowser("https://c.example", "t1")
	wantEqual(t, r.Browsers, []BrowserGrant{{Origin: "https://c.example", GrantedAt: "t0"}})
}

func TestSelectApps(t *testing.T) {
	two := Empty().AddApp(appA).AddApp(appB)
	if got, err := two.SelectApps(nil); err != nil {
		t.Fatal(err)
	} else {
		wantEqual(t, got, []App{appA, appB})
	}
	if got, err := two.SelectApps(ptr("https://b.example/")); err != nil {
		t.Fatal(err)
	} else {
		wantEqual(t, got, []App{appB})
	}
	if _, err := two.SelectApps(ptr("https://x.example")); err == nil || !strings.Contains(err.Error(), "not paired with https://x.example") {
		t.Errorf("error = %v, want not paired with https://x.example", err)
	}
	if _, err := two.SelectApps(ptr("nonsense")); err == nil || !strings.Contains(err.Error(), "is not a URL") {
		t.Errorf("error = %v, want is not a URL", err)
	}
}

func TestSelectAppTakesTheOnlyAppOrTheOneNamed(t *testing.T) {
	two := Empty().AddApp(appA).AddApp(appB)
	if got, err := Empty().AddApp(appA).SelectApp(nil); err != nil || got != appA {
		t.Errorf("only app = %+v, %v", got, err)
	}
	if got, err := two.SelectApp(ptr(appB.Server)); err != nil || got != appB {
		t.Errorf("named app = %+v, %v", got, err)
	}
}

func TestSelectAppWithSeveralAppsAndNoServerAsksForApp(t *testing.T) {
	two := Empty().AddApp(appA).AddApp(appB)
	if _, err := two.SelectApp(nil); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Errorf("error = %v, want it to mention --app", err)
	}
	if _, err := Empty().SelectApp(nil); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Errorf("error = %v, want not paired", err)
	}
}

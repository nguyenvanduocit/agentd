package registry

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	appA = App{Server: "https://a.example", Token: "ta"}
	appB = App{Server: "https://b.example", Token: "tb"}
)

func ptr(s string) *string { return &s }

// day0 is when the grants of these tests were made.
var day0 = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

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
	r := Empty().AddApp(App{Server: "https://a.example", Token: "t<&>"}).AddBrowser("https://c.example", day0, Access{})
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
	r := Empty().AddApp(appA).AddApp(appB).AddBrowser("https://c.example", day0, Access{})
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
	later := day0.Add(time.Hour)
	r := Empty().AddApp(appA).AddApp(appB).AddBrowser(appA.Server, day0, Access{}).AddBrowser("https://other.example", later, Access{})
	r = r.RemoveOrigin(appA.Server)
	wantEqual(t, r.Apps, []App{appB})
	wantEqual(t, r.Browsers, []BrowserGrant{{Origin: "https://other.example", GrantedAt: GrantStamp(later)}})
}

func TestHasAppAndHasBrowserAnswerByOrigin(t *testing.T) {
	r := Empty().AddApp(appA).AddBrowser("https://c.example", day0, Access{})
	now := day0.Add(time.Hour)
	for _, tc := range []struct {
		name string
		got  bool
		want bool
	}{
		{"app a", r.HasApp(appA.Server, day0), true},
		{"app b", r.HasApp(appB.Server, day0), false},
		{"browser c", r.HasBrowser("https://c.example", now), true},
		{"browser a", r.HasBrowser(appA.Server, now), false},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestWithdrawnIsWhatRunsForAnOriginTheRegistryNoLongerHas(t *testing.T) {
	r := Empty().AddApp(appA).AddBrowser("https://c.example", day0, Access{})
	now := day0.Add(time.Hour)
	wantEqual(t, r.Withdrawn([]string{appA.Server, appB.Server}, []string{"https://c.example", "https://d.example"}, now),
		Ended{Apps: []string{appB.Server}, Pages: []string{"https://d.example"}})
	wantEqual(t, r.Withdrawn([]string{appA.Server}, []string{"https://c.example"}, now), Ended{Apps: []string{}, Pages: []string{}})
}

func TestARepeatedBrowserGrantIsStoredOnce(t *testing.T) {
	r := Empty().AddBrowser("https://c.example", day0, Access{}).AddBrowser("https://c.example", day0.Add(time.Hour), Access{})
	wantEqual(t, r.Browsers, []BrowserGrant{{Origin: "https://c.example", GrantedAt: GrantStamp(day0)}})
}

func TestSelectApps(t *testing.T) {
	two := Empty().AddApp(appA).AddApp(appB)
	if got, err := two.SelectApps(nil, day0); err != nil {
		t.Fatal(err)
	} else {
		wantEqual(t, got, []App{appA, appB})
	}
	if got, err := two.SelectApps(ptr("https://b.example/"), day0); err != nil {
		t.Fatal(err)
	} else {
		wantEqual(t, got, []App{appB})
	}
	if _, err := two.SelectApps(ptr("https://x.example"), day0); err == nil || !strings.Contains(err.Error(), "not paired with https://x.example") {
		t.Errorf("error = %v, want not paired with https://x.example", err)
	}
	if _, err := two.SelectApps(ptr("nonsense"), day0); err == nil || !strings.Contains(err.Error(), "is not a URL") {
		t.Errorf("error = %v, want is not a URL", err)
	}
}

func TestSelectAppTakesTheOnlyAppOrTheOneNamed(t *testing.T) {
	two := Empty().AddApp(appA).AddApp(appB)
	if got, err := Empty().AddApp(appA).SelectApp(nil, day0); err != nil || !reflect.DeepEqual(got, appA) {
		t.Errorf("only app = %+v, %v", got, err)
	}
	if got, err := two.SelectApp(ptr(appB.Server), day0); err != nil || !reflect.DeepEqual(got, appB) {
		t.Errorf("named app = %+v, %v", got, err)
	}
}

func TestSelectAppWithSeveralAppsAndNoServerAsksForApp(t *testing.T) {
	two := Empty().AddApp(appA).AddApp(appB)
	if _, err := two.SelectApp(nil, day0); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Errorf("error = %v, want it to mention --app", err)
	}
	if _, err := Empty().SelectApp(nil, day0); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Errorf("error = %v, want not paired", err)
	}
}

func TestFoldersRoundTripAndAreLookedUpPerGrant(t *testing.T) {
	r := Empty().
		AddApp(App{Server: "https://a.example", Token: "ta", Folders: []string{"/work/a", "/work/b"}}).
		AddBrowser("https://c.example", day0, Access{Folders: []string{"/work/c"}}).
		AddBrowser("https://d.example", day0, Access{})
	back, err := Parse(string(r.Marshal()))
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, back, r)
	wantEqual(t, back.AppFolders("https://a.example"), []string{"/work/a", "/work/b"})
	wantEqual(t, back.BrowserFolders("https://c.example"), []string{"/work/c"})
	if back.BrowserFolders("https://d.example") != nil || back.BrowserFolders("https://unknown.example") != nil || back.AppFolders("https://unknown.example") != nil {
		t.Error("a grant without folders, or none at all, has no folders")
	}
	if strings.Contains(string(Empty().AddBrowser("https://d.example", day0, Access{}).Marshal()), "folders") {
		t.Error("a grant without folders is written as before")
	}
}

func TestParseRefusesFoldersThatAreNotAListOfText(t *testing.T) {
	for _, text := range []string{
		`{"apps":[{"server":"https://a","token":"t","folders":"/x"}]}`,
		`{"apps":[],"browsers":[{"origin":"https://a","grantedAt":"t","folders":[1]}]}`,
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s parsed", text)
		}
	}
}

func TestAGrantStampIsTheFormatTheNodeDaemonWroteAndReadsBack(t *testing.T) {
	if got := GrantStamp(day0); got != "2026-10-19T00:00:00.000Z" {
		t.Errorf("GrantStamp = %q", got)
	}
	g := BrowserGrant{Origin: "https://c.example", GrantedAt: GrantStamp(day0)}
	if at, ends, ok := g.End(); !ok || !ends || !at.Equal(day0.Add(BrowserGrantTTL)) {
		t.Errorf("End = %v, %v; want %v", at, ok, day0.Add(BrowserGrantTTL))
	}
}

func TestABrowserGrantLastsThirtyDaysFromTheDayItWasMade(t *testing.T) {
	if BrowserGrantTTL != 30*24*time.Hour {
		t.Fatalf("BrowserGrantTTL = %v, want 30 days", BrowserGrantTTL)
	}
	r := Empty().AddBrowser("https://c.example", day0, Access{})
	for _, tc := range []struct {
		name string
		at   time.Duration
		want bool
	}{
		{"when it was made", 0, true},
		{"a day later", 24 * time.Hour, true},
		{"a second before the thirtieth day ends", BrowserGrantTTL - time.Second, true},
		{"on the thirtieth day's end", BrowserGrantTTL, false},
		{"a year later", 365 * 24 * time.Hour, false},
	} {
		if got := r.HasBrowser("https://c.example", day0.Add(tc.at)); got != tc.want {
			t.Errorf("%s: HasBrowser = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// What cannot be read as a time cannot be told to be young, so it asks again.
func TestAGrantWithoutAReadableTimeIsLapsed(t *testing.T) {
	for _, at := range []string{"", "t", "yesterday", "2026-10-19", "0000-00-00T00:00:00.000Z"} {
		r := Registry{Apps: []App{}, Browsers: []BrowserGrant{{Origin: "https://c.example", GrantedAt: at}}}
		if r.HasBrowser("https://c.example", day0) || r.HasLiveBrowsers(day0) {
			t.Errorf("grantedAt %q counts as live", at)
		}
	}
}

func TestHasLiveBrowsersSaysWhetherAnyPageGrantIsYoung(t *testing.T) {
	r := Empty().AddBrowser("https://old.example", day0, Access{}).AddBrowser("https://new.example", day0.Add(20*24*time.Hour), Access{})
	if !r.HasLiveBrowsers(day0.Add(31 * 24 * time.Hour)) {
		t.Error("the younger grant is live on day 31")
	}
	if r.HasLiveBrowsers(day0.Add(51 * 24 * time.Hour)) {
		t.Error("both are lapsed on day 51")
	}
	if Empty().HasLiveBrowsers(day0) {
		t.Error("an empty registry has no live grant")
	}
}

func TestAskingAgainAfterTheGrantLapsedRenewsItWithTheNewFolders(t *testing.T) {
	other := Empty().AddBrowser("https://other.example", day0, Access{Folders: []string{"/work/o"}})
	r := other.AddBrowser("https://c.example", day0, Access{Folders: []string{"/work/old"}})
	again := day0.Add(BrowserGrantTTL + time.Hour)
	r = r.AddBrowser("https://c.example", again, Access{Folders: []string{"/work/new"}})
	wantEqual(t, r.Browsers, []BrowserGrant{
		{Origin: "https://other.example", GrantedAt: GrantStamp(day0), Folders: []string{"/work/o"}},
		{Origin: "https://c.example", GrantedAt: GrantStamp(again), Folders: []string{"/work/new"}},
	})
	if !r.HasBrowser("https://c.example", again.Add(time.Hour)) {
		t.Error("the renewed grant is live")
	}
}

func TestAGrantThatIsStillLiveKeepsItsFirstTimeAndFoldersWhenAskedAgain(t *testing.T) {
	r := Empty().AddBrowser("https://c.example", day0, Access{Folders: []string{"/work/a"}})
	r = r.AddBrowser("https://c.example", day0.Add(10*24*time.Hour), Access{Folders: []string{"/work/b"}})
	wantEqual(t, r.Browsers, []BrowserGrant{{Origin: "https://c.example", GrantedAt: GrantStamp(day0), Folders: []string{"/work/a"}}})
}

func TestPagesOfALapsedGrantAreWithdrawnLikeThoseOfARemovedOne(t *testing.T) {
	r := Empty().AddBrowser("https://old.example", day0, Access{}).AddBrowser("https://new.example", day0.Add(20*24*time.Hour), Access{})
	now := day0.Add(31 * 24 * time.Hour)
	wantEqual(t, r.Withdrawn(nil, []string{"https://old.example", "https://new.example"}, now),
		Ended{Apps: []string{}, Pages: []string{"https://old.example"}})
}

// A session that is still open when its grant lapses must stay inside its folders until the daemon ends it: no folders
// would mean no limit.
func TestALapsedGrantStillNamesItsFolders(t *testing.T) {
	r := Empty().AddBrowser("https://c.example", day0, Access{Folders: []string{"/work/c"}})
	wantEqual(t, r.BrowserFolders("https://c.example"), []string{"/work/c"})
}

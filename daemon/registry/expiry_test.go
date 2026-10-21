package registry

import (
	"strings"
	"testing"
	"time"
)

func TestExpiryStampIsWhenTheAccessEndsOrNever(t *testing.T) {
	wantEqual(t, Expiry(day0, 0), Never)
	wantEqual(t, Expiry(day0, -time.Hour), Never)
	wantEqual(t, Expiry(day0, 24*time.Hour), "2026-10-20T00:00:00Z")
	if _, err := time.Parse(time.RFC3339Nano, Expiry(day0, time.Hour)); err != nil {
		t.Errorf("the stamp does not read back as a time: %v", err)
	}
}

func TestAnAppEndsWhenItsStampSaysAndNeverWithoutOne(t *testing.T) {
	end := day0.Add(24 * time.Hour)
	for _, tc := range []struct {
		name  string
		stamp string
		now   time.Time
		want  bool
	}{
		{"no stamp is the app of before expiry existed", "", day0.Add(1000 * 24 * time.Hour), true},
		{"never", Never, day0.Add(1000 * 24 * time.Hour), true},
		{"before the end", Expiry(day0, 24*time.Hour), end.Add(-time.Second), true},
		{"at the end", Expiry(day0, 24*time.Hour), end, false},
		{"after the end", Expiry(day0, 24*time.Hour), end.Add(time.Hour), false},
		{"a stamp that is not a time cannot be told young", "soon", day0, false},
	} {
		if got := (App{Server: "https://a.example", Token: "t", ExpiresAt: tc.stamp}).Live(tc.now); got != tc.want {
			t.Errorf("%s: Live = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestABrowserGrantEndsAtItsOwnStampBeforeTheThirtyDayRule(t *testing.T) {
	short := Empty().AddBrowser("https://c.example", day0, Access{ExpiresAt: Expiry(day0, time.Hour)})
	if !short.HasBrowser("https://c.example", day0.Add(59*time.Minute)) || short.HasBrowser("https://c.example", day0.Add(time.Hour)) {
		t.Error("a one hour grant ends after one hour")
	}
	long := Empty().AddBrowser("https://c.example", day0, Access{ExpiresAt: Expiry(day0, 90*24*time.Hour)})
	if !long.HasBrowser("https://c.example", day0.Add(BrowserGrantTTL+time.Hour)) {
		t.Error("a 90 day grant outlives the thirty day rule of grants that name no end")
	}
	never := Empty().AddBrowser("https://c.example", day0, Access{ExpiresAt: Never})
	if !never.HasBrowser("https://c.example", day0.Add(10*365*24*time.Hour)) || !never.HasLiveBrowsers(day0.Add(10*365*24*time.Hour)) {
		t.Error("a grant with no end is live ten years on")
	}
}

func TestAccessKeepsItsNameAndEndThroughTheFile(t *testing.T) {
	r := Empty().
		AddApp(App{Server: "https://a.example", Token: "ta", Name: "AIOcean", ExpiresAt: Expiry(day0, 24*time.Hour)}).
		AddBrowser("https://c.example", day0, Access{Name: "Docs", ExpiresAt: Never})
	text := string(r.Marshal())
	for _, want := range []string{`"name": "AIOcean"`, `"expiresAt": "2026-10-20T00:00:00Z"`, `"name": "Docs"`, `"expiresAt": "never"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the file lacks %s:\n%s", want, text)
		}
	}
	back, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, back, r)
}

func TestAGrantWithoutANameOrEndIsWrittenAsBefore(t *testing.T) {
	text := string(Empty().AddApp(appA).AddBrowser("https://c.example", day0, Access{}).Marshal())
	if strings.Contains(text, "name") || strings.Contains(text, "expiresAt") {
		t.Errorf("a grant that names neither carries them anyway:\n%s", text)
	}
}

func TestParseRefusesANameOrEndThatIsNotText(t *testing.T) {
	for _, text := range []string{
		`{"apps":[{"server":"https://a","token":"t","name":3}]}`,
		`{"apps":[{"server":"https://a","token":"t","expiresAt":{}}]}`,
		`{"apps":[],"browsers":[{"origin":"https://c","grantedAt":"2026-10-19T00:00:00.000Z","name":[]}]}`,
		`{"apps":[],"browsers":[{"origin":"https://c","grantedAt":"2026-10-19T00:00:00.000Z","expiresAt":1}]}`,
	} {
		if _, err := Parse(text); err == nil {
			t.Errorf("Parse accepted %s", text)
		}
	}
}

func TestALapsedAppIsNotPairedAnyMore(t *testing.T) {
	r := Empty().
		AddApp(App{Server: "https://old.example", Token: "t", ExpiresAt: Expiry(day0, time.Hour)}).
		AddApp(App{Server: "https://kept.example", Token: "t", ExpiresAt: Never}).
		AddApp(appA)
	now := day0.Add(2 * time.Hour)
	if r.HasApp("https://old.example", now) || !r.HasApp("https://kept.example", now) || !r.HasApp(appA.Server, now) {
		t.Error("HasApp must be false for the lapsed app only")
	}
	if !r.HasApp("https://old.example", day0) {
		t.Error("the app is paired until its end")
	}
}

func TestALapsedAppIsWithdrawnAndNotSelected(t *testing.T) {
	r := Empty().
		AddApp(App{Server: "https://old.example", Token: "t", ExpiresAt: Expiry(day0, time.Hour)}).
		AddApp(appA)
	now := day0.Add(2 * time.Hour)
	wantEqual(t, r.Withdrawn([]string{"https://old.example", appA.Server}, nil, now), Ended{Apps: []string{"https://old.example"}, Pages: []string{}})
	apps, err := r.SelectApps(nil, now)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, apps, []App{appA})
	if _, err := r.SelectApps(ptr("https://old.example"), now); err == nil {
		t.Error("a lapsed app is named as not paired")
	}
	if _, err := r.SelectApp(nil, now); err != nil {
		t.Errorf("the one app that has not lapsed is the only app: %v", err)
	}
}

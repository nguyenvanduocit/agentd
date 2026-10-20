# release

`github.com/nguyenvanduocit/agentd/sdk/release` tells your app's server which agentd version is the newest, so that the list of a user's computers can say "update pending" for one that is behind. It reads `latest.json` from agentd's releases page, the signed file every daemon updates from, and trusts it only when a release key signed it.

It is for apps built on [`sdk/goserver`](../goserver/README.md). Nothing else needs it.

## Use

```go
func newServer(ctx context.Context, tokens goserver.Tokens) *goserver.Server {
	latest := release.Watch(ctx, nil, release.Releases, time.Hour) // reads at once, then every hour, until ctx ends
	return goserver.New(goserver.Options{Tokens: tokens, LatestVersion: latest.Version})
}
```

`latest.Version` never waits on the network: it is the version of the last offer that verified, and empty until one has. `goserver.List.LatestVersion` carries it to your page, and a computer is behind when:

```go
func behind(list goserver.List) []string {
	var names []string
	for _, d := range list.Daemons {
		if release.Older(d.Version, list.LatestVersion) {
			names = append(names, d.Name)
		}
	}
	return names
}
```

A computer updates itself, so "behind" is a hint that an update is coming, not a request to the user.

## Rules

- **`Watch(ctx, client, releases, every)`** reads in the background. An `every` of zero or less is one hour, and a nil `client` is `http.DefaultClient`. A read that fails is logged and keeps the version read before.
- **`Fetch(ctx, client, releases)`** is one read: it returns the `Offer` (`Version`, `URL`, `SHA256`, `Signature`) once its signature verifies, and an error for anything else (not found, not JSON, larger than 64 KB, wrong signature, a version that is not a plain version).
- **`Older(version, than)`** orders plain `x.y.z` releases only. A dev build, an empty version or anything else is not ordered, so it is never older: daemons order versions the same way.
- **`release.Releases`** is `https://github.com/nguyenvanduocit/agentd/releases`. A stand-in with the same layout works too (`latest/download/latest.json` redirecting to a release's file): `cmd/agentd-server -dist <dir>` serves one for development.

## Which keys it trusts

The same keys daemons check updates against: the release key (`daemon/release.pub`) and, once its owner has made one, the backup key kept offline (`daemon/release-backup.pub`). A release signed by the backup key, which is how a lost or leaked release key is replaced, verifies here as it does in a daemon.

The keys are the `*.pub` files of [`keys/`](keys), embedded in the package: `keys/release.pub` is a copy of `daemon/release.pub`. When the backup key is made, copy `daemon/release-backup.pub` to `keys/release-backup.pub` in the same commit; no code changes. A test fails until you do, and fails if a copy differs from its original.

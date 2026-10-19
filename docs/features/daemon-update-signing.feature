Feature: Apply only daemon updates signed with the release key
  A daemon installs an update only when the release key signed its version and tarball sha256, so a compromised
  release server cannot replace the daemon on machines that run it. The first install, through install.sh, is not covered.

  Scenario: Install a signed update
    Given the release offers a newer version with its sha256 and the release key's signature of both
    When the daemon checks the signature against the release key baked into its bundle
    Then it downloads the tarball, checks the sha256 and installs the new version

  Scenario Outline: Refuse an update the release key did not sign
    Given the release offers a newer version with <offer>
    When the daemon checks the offer
    Then it downloads nothing and keeps running its current version
    And it logs why it refused the update
    And it does not retry that offer until the next check of latest.json

    Examples:
      | offer                                                       |
      | no signature                                                |
      | a signature that is not 64 bytes of base64                  |
      | the signature of another version                            |
      | the signature of another sha256, matching a tampered tarball |
      | a signature by another key                                  |

  Scenario: Refuse every update without a baked release key
    Given the daemon was bundled without a release key
    When the release offers a correctly signed newer version
    Then the daemon refuses it and keeps running its current version

  Scenario: Sign every daemon build
    Given scripts/build-daemon.sh builds a daemon
    When it writes releases/latest.json
    Then latest.json carries the signature of the version and sha256
    And the bundle carries the public key of the key that signed it

  Scenario: Fail a release build signed with the wrong key
    Given RELEASE=1 is set
    When DAEMON_SIGNING_KEY is missing or its public key is not daemon/release.pub
    Then the build fails before it bundles anything

  Scenario: Serve no release without a signature of the right shape
    Given latest.json has no signature of the right shape
    When a daemon checks for an update or a user fetches install.sh
    Then latest.json and install.sh are answered with an error

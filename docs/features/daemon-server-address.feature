Feature: Connect the daemon securely to its server
  Plaintext server addresses are accepted only for loopback hosts.

  Scenario Outline: Normalize a supported server address
    Given the server address is "<address>"
    When the daemon resolves its server origin
    Then the origin is "<origin>"

    Examples:
      | address                              | origin                 |
      | https://mc.example.com/path?x=1#part   | https://mc.example.com |
      | wss://mc.example.com/socket.io        | https://mc.example.com |
      | http://127.0.0.1:8080/app              | http://127.0.0.1:8080  |
      | ws://localhost:3000                   | http://localhost:3000 |
      | http://[::1]:8080                     | http://[::1]:8080      |

  Scenario: Refuse plaintext outside loopback
    Given the server address uses http or ws with a non-loopback host
    When the daemon resolves its server origin
    Then the address is refused with an error requiring https

  Scenario: Refuse a malformed or unsupported address
    Given the server address is malformed or uses an unsupported protocol
    When the daemon resolves its server origin
    Then the daemon reports an address error

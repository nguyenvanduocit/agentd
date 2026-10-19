import { describe, expect, spyOn, test } from "bun:test"
import type { Socket } from "node:net"
import { lsofArgs, lsofOwners, peerAllowed, procNetOwners, type Connection } from "../../src/core/peer.ts"
import { peerGate } from "../../src/peer.ts"

// Captured on Linux (oven/bun:1, uid 1234): a server on 127.0.0.1:40473 (0x9E19), an IPv4 client from port 59858
// (0xE9D2) and an IPv6 client to ::ffff:127.0.0.1 from port 59870 (0xE9DE).
const PROC_TCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:9E19 00000000:0000 0A 00000000:00000001 00:00000000 00000000  1234        0 4981 2 00000000cdc04366 100 0 0 10 0
   1: 0100007F:E9D2 0100007F:9E19 01 00000000:00000000 00:00000000 00000000  1234        0 4982 2 00000000dc8c8623 20 0 0 10 -1
   2: 0100007F:9E19 0100007F:E9DE 01 00000000:00000000 00:00000000 00000000  1234        0 0 1 00000000637eb0b8 20 0 0 10 -1
   3: 0100007F:9E19 0100007F:E9D2 01 00000000:00000000 00:00000000 00000000  1234        0 9411 1 000000002b8b53c1 20 0 0 10 -1
`
const PROC_TCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF00000100007F:E9DE 0000000000000000FFFF00000100007F:9E19 01 00000000:00000000 00:00000000 00000000  1234        0 4983 2 000000005753e3de 20 0 0 10 -1
`

// Captured on macOS with `lsof -nP -w -iTCP:61811 -Fpun`: the gateway (pid 31070) on 127.0.0.1:61810 and a client
// (python, pid 31071) from port 61811.
const LSOF = "p31070\nu501\nf5\nn127.0.0.1:61810->127.0.0.1:61811\np31071\nu501\nf3\nn127.0.0.1:61811->127.0.0.1:61810\n"

const linux = (peerPort: number): Connection => ({ peerAddress: "127.0.0.1", peerPort, gatewayAddress: "127.0.0.1", gatewayPort: 40473 })
const mac: Connection = { peerAddress: "127.0.0.1", peerPort: 61811, gatewayAddress: "127.0.0.1", gatewayPort: 61810 }

describe("procNetOwners (/proc/net/tcp, tcp6)", () => {
  test("the uid of the peer's own socket, not the gateway's end of the connection", () => {
    const text = PROC_TCP.replace("  1234        0 4982", "  1000        0 4982")
    expect(procNetOwners(text, linux(59858))).toEqual([1000])
  })

  test("an IPv4-mapped row of tcp6", () => {
    const text = PROC_TCP6.replace("1234", "1000")
    expect(procNetOwners(text, linux(59870))).toEqual([1000])
    expect(procNetOwners(PROC_TCP, linux(59870))).toEqual([])
  })

  test("another loopback source address is encoded like the kernel does", () => {
    const text = PROC_TCP.replace("0100007F:E9D2 0100007F:9E19", "0200007F:E9D2 0100007F:9E19")
    expect(procNetOwners(text, { ...linux(59858), peerAddress: "127.0.0.2" })).toEqual([1234])
    expect(procNetOwners(text, linux(59858))).toEqual([])
  })

  test("no row of the connection, a TIME_WAIT row, malformed lines and a non-IPv4 address give no owner", () => {
    expect(procNetOwners(PROC_TCP, linux(1))).toEqual([])
    expect(procNetOwners(PROC_TCP.replace("0100007F:9E19 01 00000000:00000000 00:00000000 00000000  1234        0 4982", "0100007F:9E19 06 00000000:00000000 00:00000000 00000000     0        0 4982"), linux(59858))).toEqual([])
    expect(procNetOwners("garbage\n   1: 0100007F:E9D2 0100007F:9E19 01 x x x x\n   1: 0100007F:E9D2\n", linux(59858))).toEqual([])
    expect(procNetOwners("", linux(59858))).toEqual([])
    expect(procNetOwners(PROC_TCP, { ...linux(59858), peerAddress: "::1" })).toEqual([])
  })
})

describe("lsofOwners (macOS)", () => {
  test("the uid of the process that holds the peer's end", () => {
    expect(lsofOwners(LSOF.replace("p31071\nu501", "p31071\nu502"), mac)).toEqual([502])
  })

  test("every process holding the socket counts", () => {
    const text = `${LSOF}p31072\nu0\nf4\nn127.0.0.1:61811->127.0.0.1:61810\n`
    expect(lsofOwners(text, mac)).toEqual([501, 0])
  })

  test("no socket of the connection, an empty answer and malformed lines give no owner", () => {
    expect(lsofOwners(LSOF, { ...mac, peerPort: 1 })).toEqual([])
    expect(lsofOwners(LSOF, { ...mac, gatewayPort: 1 })).toEqual([])
    expect(lsofOwners("", mac)).toEqual([])
    expect(lsofOwners("n127.0.0.1:61811->127.0.0.1:61810\n", mac)).toEqual([])
    expect(lsofOwners("p1\nuroot\nn127.0.0.1:61811->127.0.0.1:61810\n", mac)).toEqual([])
  })

  test("asks for the TCP sockets of the peer's port only, without resolving names", () => {
    expect(lsofArgs(61811)).toEqual(["-nP", "-w", "-iTCP:61811", "-Fpun"])
  })
})

describe("peerAllowed", () => {
  test("only when every owner found is this user", () => {
    expect(peerAllowed([501], 501)).toBe(true)
    expect(peerAllowed([501, 501], 501)).toBe(true)
    expect(peerAllowed([502], 501)).toBe(false)
    expect(peerAllowed([501, 0], 501)).toBe(false)
  })

  test("no owner found, or no user id for this process, refuses", () => {
    expect(peerAllowed([], 501)).toBe(false)
    expect(peerAllowed([501], undefined)).toBe(false)
  })
})

describe("peerGate", () => {
  const socket = (peerPort: number) => ({ remoteAddress: "127.0.0.1", remotePort: peerPort, localAddress: "127.0.0.1", localPort: 47615 }) as Socket

  test("asks once per connection: keep-alive requests share the answer", async () => {
    const asked: Connection[] = []
    const gate = peerGate((c) => {
      asked.push(c)
      return Promise.resolve([501])
    }, 501)
    const a = socket(50000)
    expect(await gate(a)).toBe(true)
    expect(await gate(a)).toBe(true)
    expect(await gate(socket(50001))).toBe(true)
    expect(asked).toEqual([
      { peerAddress: "127.0.0.1", peerPort: 50000, gatewayAddress: "127.0.0.1", gatewayPort: 47615 },
      { peerAddress: "127.0.0.1", peerPort: 50001, gatewayAddress: "127.0.0.1", gatewayPort: 47615 },
    ])
  })

  test("another user, a closed socket and a failing lookup are refused; the failure is logged once", async () => {
    const stderr = spyOn(process.stderr, "write").mockImplementation(() => true)
    try {
      expect(await peerGate(() => Promise.resolve([502]), 501)(socket(50000))).toBe(false)
      expect(await peerGate(() => Promise.resolve([501]), 501)({} as Socket)).toBe(false)
      const broken = peerGate(() => Promise.reject(new Error("lsof is gone")), 501)
      expect(await broken(socket(50000))).toBe(false)
      expect(await broken(socket(50001))).toBe(false)
      expect(stderr.mock.calls.map((c) => String(c[0]))).toEqual([expect.stringContaining("lsof is gone") as string])
    } finally {
      stderr.mockRestore()
    }
  })
})

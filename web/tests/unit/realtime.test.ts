import { memoryKV } from "../../src/lib/idb";
import { Realtime, type SocketLike } from "../../src/lib/realtime";
import type { RealtimeEvent } from "../../src/lib/types";

class FakeSocket implements SocketLike {
  onopen: SocketLike["onopen"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  closed = false;
  constructor(public url: string) {}
  open() { this.onopen?.({}); }
  send(ev: Partial<RealtimeEvent>) { this.onmessage?.({ data: JSON.stringify(ev) }); }
  close() { this.closed = true; this.onclose?.({}); }
}

const ev = (type: RealtimeEvent["type"], seq: number, payload = {}): RealtimeEvent =>
  ({ event_id: String(seq), type, seq, occurred_at: "2026-09-29T00:00:00Z", payload });

function setup(cfg = { backoffStartMs: 100, backoffMaxMs: 400, silenceMs: 0 }) {
  const sockets: FakeSocket[] = [];
  const kv = memoryKV();
  const rt = new Realtime(() => "ws://x/api/v1/ws", kv, (u) => { const s = new FakeSocket(u); sockets.push(s); return s; }, cfg);
  const seen: RealtimeEvent[] = [];
  rt.on((e) => seen.push(e));
  return { rt, kv, sockets, seen };
}

test("snapshot, then events by seq; duplicates and stale events are dropped (ADR-23)", async () => {
  const { rt, kv, seen } = setup();
  await rt.handle(ev("system.snapshot", 10, { wallets: [] }));
  expect(await rt.handle(ev("task.status", 9))).toBe(false); // старше снапшота
  expect(await rt.handle(ev("task.status", 11))).toBe(true);
  expect(await rt.handle(ev("task.status", 11))).toBe(false); // дубль
  expect(await rt.handle(ev("wallet.changed", 12))).toBe(true);
  expect(seen.map((e) => e.seq)).toEqual([10, 11, 12]);
  expect(await kv.get("state", "last_seq")).toBe(12);
  expect((await kv.all("events")).length).toBe(2); // применённые события — сразу в локальной базе
});

test("progress is ephemeral: delivered, not stored, not deduplicated", async () => {
  const { rt, kv, seen } = setup();
  await rt.handle(ev("system.snapshot", 5));
  await rt.handle(ev("task.progress", 5, { task_id: "t", percent: 30, phase: "x" }));
  await rt.handle(ev("task.progress", 5, { task_id: "t", percent: 60, phase: "y" }));
  expect(seen.filter((e) => e.type === "task.progress")).toHaveLength(2);
  expect(await kv.all("events")).toHaveLength(0);
});

test("applied seq survives restart (local base)", async () => {
  const { rt, kv } = setup();
  await rt.handle(ev("system.snapshot", 3));
  await rt.handle(ev("task.status", 4));
  const again = new Realtime(() => "ws://x", kv, (u) => new FakeSocket(u));
  await again.start();
  expect(again.appliedSeq).toBe(4);
  again.stop();
});

test("reconnect backoff doubles up to the ceiling; check-now resets it", async () => {
  vi.useFakeTimers();
  const { rt, sockets } = setup();
  const states: string[] = [];
  rt.onState((s) => states.push(s));
  await rt.start();
  sockets[0].open();
  expect(rt.state).toBe("online");
  const waits: number[] = [];
  for (let i = 0; i < 4; i++) {
    const before = sockets.length;
    sockets.at(-1)!.close();
    let waited = 0;
    while (sockets.length === before) { vi.advanceTimersByTime(50); waited += 50; }
    waits.push(waited);
  }
  expect(waits).toEqual([100, 200, 400, 400]); // рост паузы до потолка — сервер не бомбим
  sockets.at(-1)!.close();
  rt.checkNow(); // «Проверить сеть» — сразу, вне таймера
  expect(sockets.at(-1)!.closed).toBe(false);
  expect(states).toContain("offline");
  rt.stop();
  vi.useRealTimers();
});

test("garbage in the channel does not break the client", async () => {
  const { rt, sockets, seen } = setup();
  await rt.start();
  sockets[0].open();
  sockets[0].onmessage?.({ data: "not json" });
  sockets[0].send(ev("system.snapshot", 1));
  // Событие применено только после записи в локальную базу — ждём её.
  await vi.waitFor(() => expect(seen.map((e) => e.type)).toContain("system.snapshot"));
  rt.stop();
});

// Realtime по ADR-23/46: snapshot → догоняющие события по seq; дубли и события старше
// применённого отбрасываются; применённое сразу пишется в локальную базу; при обрыве —
// переподключение с растущей паузой до потолка; «Проверить сеть» — немедленная попытка.
// Все тайминги — в конфиге.

import type { KV } from "./idb";
import type { RealtimeEvent } from "./types";

export interface RealtimeConfig {
  backoffStartMs: number;
  backoffMaxMs: number; // потолок (ADR-23: базово 60 с)
  // Тишина дольше — соединение мёртво (0 — выключено). Ядро пингует на уровне протокола WebSocket,
  // браузер отвечает сам и JS этих пингов не видит; мёртвое соединение закрывает сервер по пропуску
  // pong, и клиент получает onclose. Порог нужен только при прикладном heartbeat в канале.
  silenceMs: number;
}

export const DEFAULT_CONFIG: RealtimeConfig = { backoffStartMs: 1000, backoffMaxMs: 60_000, silenceMs: 0 };

export type NetState = "online" | "connecting" | "offline";

export interface SocketLike {
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: unknown) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  close(): void;
}

type Listener = (ev: RealtimeEvent) => void;

export class Realtime {
  private socket: SocketLike | null = null;
  private lastSeq = 0;
  private backoff: number;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private silence: ReturnType<typeof setTimeout> | null = null;
  private listeners = new Set<Listener>();
  private stateListeners = new Set<(s: NetState) => void>();
  private stopped = true;
  state: NetState = "offline";

  constructor(
    private url: () => string | null,
    private kv: KV,
    private makeSocket: (url: string) => SocketLike,
    private cfg: RealtimeConfig = DEFAULT_CONFIG,
  ) {
    this.backoff = cfg.backoffStartMs;
  }

  on(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  onState(fn: (s: NetState) => void): () => void {
    this.stateListeners.add(fn);
    return () => this.stateListeners.delete(fn);
  }

  get appliedSeq(): number {
    return this.lastSeq;
  }

  async start(): Promise<void> {
    this.stopped = false;
    this.lastSeq = (await this.kv.get<number>("state", "last_seq")) ?? 0;
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    this.clearTimers();
    this.socket?.close();
    this.socket = null;
    this.setState("offline");
  }

  /** Кнопка «Проверить сеть»: вне таймера backoff, пауза сбрасывается. */
  checkNow(): void {
    if (this.stopped) return;
    this.backoff = this.cfg.backoffStartMs;
    this.clearTimers();
    this.socket?.close();
    this.connect();
  }

  private setState(s: NetState): void {
    this.state = s;
    this.stateListeners.forEach((fn) => fn(s));
  }

  private clearTimers(): void {
    if (this.timer) clearTimeout(this.timer);
    if (this.silence) clearTimeout(this.silence);
    this.timer = this.silence = null;
  }

  private connect(): void {
    const url = this.url();
    if (!url) {
      this.setState("offline");
      return;
    }
    this.setState("connecting");
    const ws = this.makeSocket(url);
    this.socket = ws;
    ws.onopen = () => {
      this.backoff = this.cfg.backoffStartMs;
      this.setState("online");
      this.touch();
    };
    ws.onmessage = (ev) => {
      this.touch();
      try {
        void this.handle(JSON.parse(String(ev.data)) as RealtimeEvent);
      } catch {
        /* мусор в канале не роняет клиент */
      }
    };
    ws.onclose = () => {
      if (this.socket !== ws) return;
      this.socket = null;
      this.scheduleReconnect();
    };
    ws.onerror = () => ws.close();
  }

  private touch(): void {
    if (this.silence) clearTimeout(this.silence);
    if (this.cfg.silenceMs <= 0) return;
    this.silence = setTimeout(() => this.socket?.close(), this.cfg.silenceMs);
  }

  private scheduleReconnect(): void {
    if (this.stopped) return;
    this.setState("offline");
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => this.connect(), this.backoff);
    this.backoff = Math.min(this.backoff * 2, this.cfg.backoffMaxMs);
  }

  /** Применение события: seq-дедуп, запись в локальную базу, уведомление экранов. */
  async handle(ev: RealtimeEvent): Promise<boolean> {
    if (ev.type === "task.progress") {
      this.listeners.forEach((fn) => fn(ev)); // эфемерно: не хранится и не нумеруется
      return true;
    }
    if (ev.type === "system.snapshot") {
      this.lastSeq = ev.seq;
      await this.kv.put("state", "snapshot", ev.payload);
      await this.kv.put("state", "last_seq", ev.seq);
      this.listeners.forEach((fn) => fn(ev));
      return true;
    }
    if (ev.seq <= this.lastSeq) return false; // дубль или старше применённого
    this.lastSeq = ev.seq;
    await this.kv.put("events", String(ev.seq).padStart(12, "0"), ev);
    await this.kv.put("state", "last_seq", ev.seq);
    this.listeners.forEach((fn) => fn(ev));
    return true;
  }
}

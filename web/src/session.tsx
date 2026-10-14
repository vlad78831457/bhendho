// Сессия: API, локальная база пользователя и realtime. Экраны подписываются на «тик»
// (каждое применённое событие) и перечитывают свои данные; прогресс задач — только в памяти.

import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { Api } from "./lib/api";
import { openKV, type KV } from "./lib/idb";
import { Realtime, type NetState, type SocketLike } from "./lib/realtime";
import type { RealtimeEvent } from "./lib/types";

export interface Progress {
  percent: number;
  phase: string;
}

interface Session {
  api: Api;
  user: { user_id: string; email: string; role?: string } | null;
  kv: KV | null;
  realtime: Realtime | null;
  net: NetState;
  tick: number;
  progress: Record<string, Progress>;
  lastEvent: RealtimeEvent | null;
}

const Ctx = createContext<Session | null>(null);

export function useSession(): Session {
  const s = useContext(Ctx);
  if (!s) throw new Error("SessionProvider is missing");
  return s;
}

function wsUrl(token: string | null): string | null {
  if (!token) return null;
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}/api/v1/ws?access_token=${encodeURIComponent(token)}`;
}

export function SessionProvider({ api, children }: { api: Api; children: ReactNode }) {
  const [authed, setAuthed] = useState(api.authed);
  const [user, setUser] = useState<Session["user"]>(null);
  const [net, setNet] = useState<NetState>("offline");
  const [tick, setTick] = useState(0);
  const [progress, setProgress] = useState<Record<string, Progress>>({});
  const [lastEvent, setLastEvent] = useState<RealtimeEvent | null>(null);
  const [rt, setRt] = useState<{ kv: KV; realtime: Realtime } | null>(null);

  useEffect(() => {
    api.onAuthChange = setAuthed;
  }, [api]);

  useEffect(() => {
    if (!authed) {
      setUser(null);
      return;
    }
    api.me().then(setUser).catch(() => setUser(null));
  }, [api, authed]);

  useEffect(() => {
    if (!user) return;
    const kv = openKV(user.user_id);
    // WebSocket браузера — частный случай SocketLike (обработчики DOM принимают конкретные Event).
    const realtime = new Realtime(() => wsUrl(api.accessToken), kv, (url) => new WebSocket(url) as unknown as SocketLike);
    const offState = realtime.onState(setNet);
    const offEvent = realtime.on((ev) => {
      setLastEvent(ev);
      if (ev.type === "task.progress") {
        const p = ev.payload as { task_id: string; percent: number; phase: string };
        setProgress((m) => ({ ...m, [p.task_id]: { percent: p.percent, phase: p.phase } }));
      } else {
        setTick((t) => t + 1);
      }
    });
    void realtime.start();
    setRt({ kv, realtime });
    const online = () => realtime.checkNow();
    window.addEventListener("online", online);
    return () => {
      window.removeEventListener("online", online);
      offState();
      offEvent();
      realtime.stop();
      setRt(null);
    };
  }, [api, user]);

  const value = useMemo<Session>(
    () => ({ api, user, kv: rt?.kv ?? null, realtime: rt?.realtime ?? null, net, tick, progress, lastEvent }),
    [api, user, rt, net, tick, progress, lastEvent],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

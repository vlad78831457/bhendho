// Локальная база пользователя (ADR-23 п.5): применённое событие сразу пишется сюда,
// экраны читают состояние отсюда. Черновики форм — тоже здесь (ADR-42: начатое не теряется).
// Без IndexedDB (приватный режим, старый браузер) — работа в памяти, без падений.

type Store = "state" | "events" | "drafts";
const DB = "offgrid";
const VERSION = 1;

export interface KV {
  get<T>(store: Store, key: string): Promise<T | undefined>;
  put(store: Store, key: string, value: unknown): Promise<void>;
  del(store: Store, key: string): Promise<void>;
  all<T>(store: Store): Promise<T[]>;
}

class MemoryKV implements KV {
  private data = new Map<string, Map<string, unknown>>();
  private s(store: Store): Map<string, unknown> {
    let m = this.data.get(store);
    if (!m) this.data.set(store, (m = new Map()));
    return m;
  }
  async get<T>(store: Store, key: string) {
    return this.s(store).get(key) as T | undefined;
  }
  async put(store: Store, key: string, value: unknown) {
    this.s(store).set(key, value);
  }
  async del(store: Store, key: string) {
    this.s(store).delete(key);
  }
  async all<T>(store: Store) {
    return [...this.s(store).values()] as T[];
  }
}

class IndexedKV implements KV {
  private db: Promise<IDBDatabase>;

  constructor(name: string) {
    this.db = new Promise((resolve, reject) => {
      const req = indexedDB.open(name, VERSION);
      req.onupgradeneeded = () => {
        for (const s of ["state", "events", "drafts"]) req.result.createObjectStore(s);
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  }

  private async run<T>(store: Store, mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest): Promise<T> {
    const db = await this.db;
    return new Promise<T>((resolve, reject) => {
      const req = fn(db.transaction(store, mode).objectStore(store));
      req.onsuccess = () => resolve(req.result as T);
      req.onerror = () => reject(req.error);
    });
  }

  get<T>(store: Store, key: string) {
    return this.run<T | undefined>(store, "readonly", (s) => s.get(key));
  }
  async put(store: Store, key: string, value: unknown) {
    await this.run(store, "readwrite", (s) => s.put(value, key));
  }
  async del(store: Store, key: string) {
    await this.run(store, "readwrite", (s) => s.delete(key));
  }
  all<T>(store: Store) {
    return this.run<T[]>(store, "readonly", (s) => s.getAll());
  }
}

/** База конкретного пользователя: данные разных аккаунтов на одном устройстве не смешиваются. */
export function openKV(userId: string): KV {
  try {
    if (typeof indexedDB !== "undefined") return new IndexedKV(`${DB}.${userId}`);
  } catch {
    /* падаем на память */
  }
  return new MemoryKV();
}

export function memoryKV(): KV {
  return new MemoryKV();
}

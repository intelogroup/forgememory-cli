export type DisposeReason = 'set' | 'delete' | 'stale' | 'evict';
export type DisposeFunction<K, V> = (val: V, key: K, reason: DisposeReason) => unknown;
export type TimeInMilliseconds = number;
export type TTLCacheOptions<K, V> = {
    /** the maximum number of items to store in the cache */
    max?: number;
    /** time in ms to store items, must be positive number */
    ttl?: TimeInMilliseconds;
    /** Update the remaining TTL when getting items */
    updateAgeOnGet?: boolean;
    /**
     * Check the remaining age when getting items. Note that if this is not
     * set, then it's possible to get expired items that have not yet been
     * preemptively purged.
     */
    checkAgeOnGet?: boolean;
    /** Update the remaining TTL when checking for an item's presence */
    updateAgeOnHas?: boolean;
    /**
     * Check the remaining age when checking for an items presence. Note that if
     * this is not set, then expired items will return `true` if they have not yet
     * been preemptively purged.
     */
    checkAgeOnHas?: boolean;
    /** do not update the TTL when setting a new value for an existing key */
    noUpdateTTL?: boolean;
    /** A function to call when an item is removed from the cache */
    dispose?: DisposeFunction<K, V>;
    /** Do not call `dispose` when setting an existing key to a new value */
    noDisposeOnSet?: boolean;
};
export type SetOptions<K, V> = Pick<TTLCacheOptions<K, V>, 'ttl' | 'noUpdateTTL' | 'noDisposeOnSet'>;
export type GetOptions<K, V> = Pick<TTLCacheOptions<K, V>, 'updateAgeOnGet' | 'ttl' | 'checkAgeOnGet'>;
export type HasOptions<K, V> = Pick<TTLCacheOptions<K, V>, 'updateAgeOnHas' | 'ttl' | 'checkAgeOnHas'>;
export declare class TTLCache<K = unknown, V = unknown> {
    expirations: Record<number, K[]>;
    data: Map<K, V>;
    expirationMap: Map<K, number>;
    ttl?: number;
    max: number;
    updateAgeOnGet: boolean;
    updateAgeOnHas: boolean;
    noUpdateTTL: boolean;
    noDisposeOnSet: boolean;
    checkAgeOnGet: boolean;
    checkAgeOnHas: boolean;
    dispose: DisposeFunction<K, V>;
    timer?: ReturnType<typeof setTimeout>;
    timerExpiration?: number;
    immortalKeys: Set<K>;
    constructor({ max, ttl, updateAgeOnGet, checkAgeOnGet, updateAgeOnHas, checkAgeOnHas, noUpdateTTL, dispose, noDisposeOnSet, }?: TTLCacheOptions<K, V>);
    setTimer(expiration: number, ttl: number): void;
    cancelTimer(): void;
    cancelTimers(): void;
    clear(): void;
    setTTL(key: K, ttl?: number | undefined): void;
    set(key: K, val: V, { ttl, noUpdateTTL, noDisposeOnSet, }?: SetOptions<K, V>): this;
    has(key: K, { checkAgeOnHas, ttl, updateAgeOnHas, }?: HasOptions<K, V>): boolean;
    getRemainingTTL(key: K): number;
    get(key: K, { updateAgeOnGet, ttl, checkAgeOnGet, }?: GetOptions<K, V>): V | undefined;
    delete(key: K): boolean;
    purgeToCapacity(): void;
    get size(): number;
    purgeStale(): void;
    entries(): Generator<[K, V], void, unknown>;
    keys(): Generator<K, void, unknown>;
    values(): Generator<V, void, unknown>;
    [Symbol.iterator](): Generator<[K, V], void, unknown>;
}
//# sourceMappingURL=index.d.ts.map
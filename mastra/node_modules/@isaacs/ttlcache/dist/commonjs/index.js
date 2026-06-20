"use strict";
// A simple TTL cache with max capacity option, ms resolution,
// autopurge, and reasonably optimized performance
// Relies on the fact that integer Object keys are kept sorted,
// and managed very efficiently by V8.
Object.defineProperty(exports, "__esModule", { value: true });
exports.TTLCache = void 0;
//oxlint-disable eslint/guard-for-in
/* c8 ignore start */
const perf = (typeof performance === 'object' &&
    performance &&
    typeof performance.now === 'function') ?
    performance
    : Date;
/* c8 ignore stop */
const now = () => perf.now();
const isPosInt = (n) => !!n && n === Math.floor(n) && n > 0 && isFinite(n);
const isPosIntOrInf = (n) => n === Infinity || isPosInt(n);
const TIMER_MAX = 2 ** 31 - 1;
class TTLCache {
    expirations = Object.create(null);
    data = new Map();
    expirationMap = new Map();
    ttl;
    max;
    updateAgeOnGet;
    updateAgeOnHas;
    noUpdateTTL;
    noDisposeOnSet;
    checkAgeOnGet;
    checkAgeOnHas;
    dispose;
    timer;
    timerExpiration;
    immortalKeys = new Set();
    constructor({ max = Infinity, ttl, updateAgeOnGet = false, checkAgeOnGet = false, updateAgeOnHas = false, checkAgeOnHas = false, noUpdateTTL = false, dispose, noDisposeOnSet = false, } = {}) {
        if (ttl !== undefined && !isPosIntOrInf(ttl)) {
            throw new TypeError('ttl must be positive integer or Infinity if set');
        }
        if (!isPosIntOrInf(max)) {
            throw new TypeError('max must be positive integer or Infinity');
        }
        this.ttl = ttl;
        this.max = max;
        this.updateAgeOnGet = !!updateAgeOnGet;
        this.checkAgeOnGet = !!checkAgeOnGet;
        this.updateAgeOnHas = !!updateAgeOnHas;
        this.checkAgeOnHas = !!checkAgeOnHas;
        this.noUpdateTTL = !!noUpdateTTL;
        this.noDisposeOnSet = !!noDisposeOnSet;
        if (dispose !== undefined) {
            if (typeof dispose !== 'function') {
                throw new TypeError('dispose must be function if set');
            }
            this.dispose = dispose;
        }
        else {
            this.dispose = (_, __, ___) => { };
        }
        this.timer = undefined;
        this.timerExpiration = undefined;
    }
    setTimer(expiration, ttl) {
        if (this.timerExpiration && this.timerExpiration < expiration) {
            return;
        }
        if (this.timer) {
            clearTimeout(this.timer);
        }
        const t = setTimeout(() => {
            this.timer = undefined;
            this.timerExpiration = undefined;
            this.purgeStale();
            for (const exp in this.expirations) {
                const e = Number(exp);
                this.setTimer(e, e - now());
                break;
            }
        }, Math.min(TIMER_MAX, Math.max(0, ttl)));
        /* c8 ignore start - affordance for non-node envs */
        if (t.unref)
            t.unref();
        /* c8 ignore stop */
        this.timerExpiration = expiration;
        this.timer = t;
    }
    // hang onto the timer so we can clearTimeout if all items
    // are deleted.  Deno doesn't have Timer.unref(), so it
    // hangs otherwise.
    cancelTimer() {
        if (this.timer) {
            clearTimeout(this.timer);
            this.timerExpiration = undefined;
            this.timer = undefined;
        }
    }
    /* c8 ignore start */
    cancelTimers() {
        process.emitWarning('TTLCache.cancelTimers has been renamed to ' +
            'TTLCache.cancelTimer (no "s"), and will be removed in the next ' +
            'major version update');
        return this.cancelTimer();
    }
    /* c8 ignore stop */
    clear() {
        const entries = this.dispose !== TTLCache.prototype.dispose ? [...this] : [];
        this.data.clear();
        this.expirationMap.clear();
        // no need for any purging now
        this.cancelTimer();
        this.expirations = Object.create(null);
        for (const [key, val] of entries) {
            this.dispose(val, key, 'delete');
        }
    }
    setTTL(key, ttl = this.ttl) {
        const current = this.expirationMap.get(key);
        if (current !== undefined) {
            // remove from the expirations list, so it isn't purged
            const exp = this.expirations[current];
            if (!exp || exp.length <= 1) {
                delete this.expirations[current];
            }
            else {
                this.expirations[current] = exp.filter(k => k !== key);
            }
        }
        if (ttl && ttl !== Infinity) {
            this.immortalKeys.delete(key);
            const expiration = Math.floor(now() + ttl);
            this.expirationMap.set(key, expiration);
            if (!this.expirations[expiration]) {
                this.expirations[expiration] = [];
                this.setTimer(expiration, ttl);
            }
            this.expirations[expiration].push(key);
        }
        else {
            this.immortalKeys.add(key);
            this.expirationMap.set(key, Infinity);
        }
    }
    set(key, val, { ttl = this.ttl, noUpdateTTL = this.noUpdateTTL, noDisposeOnSet = this.noDisposeOnSet, } = {}) {
        if (!isPosIntOrInf(ttl)) {
            throw new TypeError('ttl must be positive integer or Infinity');
        }
        if (this.expirationMap.has(key)) {
            if (!noUpdateTTL) {
                this.setTTL(key, ttl);
            }
            // has old value
            const oldValue = this.data.get(key);
            const disp = !noDisposeOnSet && this.data.has(key);
            if (oldValue !== val) {
                this.data.set(key, val);
                if (disp) {
                    this.dispose(oldValue, key, 'set');
                }
            }
        }
        else {
            this.setTTL(key, ttl);
            this.data.set(key, val);
        }
        while (this.size > this.max) {
            this.purgeToCapacity();
        }
        return this;
    }
    has(key, { checkAgeOnHas = this.checkAgeOnHas, ttl = this.ttl, updateAgeOnHas = this.updateAgeOnHas, } = {}) {
        if (this.data.has(key)) {
            if (checkAgeOnHas && this.getRemainingTTL(key) === 0) {
                this.delete(key);
                return false;
            }
            if (updateAgeOnHas) {
                this.setTTL(key, ttl);
            }
            return true;
        }
        return false;
    }
    getRemainingTTL(key) {
        const expiration = this.expirationMap.get(key);
        return (expiration === Infinity ? expiration
            : expiration !== undefined ?
                Math.max(0, Math.ceil(expiration - now()))
                : 0);
    }
    get(key, { updateAgeOnGet = this.updateAgeOnGet, ttl = this.ttl, checkAgeOnGet = this.checkAgeOnGet, } = {}) {
        const val = this.data.get(key);
        if (checkAgeOnGet && this.getRemainingTTL(key) === 0) {
            this.delete(key);
            return undefined;
        }
        if (updateAgeOnGet) {
            this.setTTL(key, ttl);
        }
        return val;
    }
    delete(key) {
        const current = this.expirationMap.get(key);
        if (current !== undefined) {
            const value = this.data.get(key);
            this.data.delete(key);
            this.expirationMap.delete(key);
            this.immortalKeys.delete(key);
            const exp = this.expirations[current];
            if (exp) {
                if (exp.length <= 1) {
                    delete this.expirations[current];
                }
                else {
                    this.expirations[current] = exp.filter(k => k !== key);
                }
            }
            this.dispose(value, key, 'delete');
            if (this.size === 0) {
                this.cancelTimer();
            }
            return true;
        }
        return false;
    }
    purgeToCapacity() {
        for (const exp in this.expirations) {
            const keys = this.expirations[exp];
            if (this.size - keys.length >= this.max) {
                delete this.expirations[exp];
                const entries = [];
                for (const key of keys) {
                    entries.push([key, this.data.get(key)]);
                    this.data.delete(key);
                    this.expirationMap.delete(key);
                }
                for (const [key, val] of entries) {
                    this.dispose(val, key, 'evict');
                }
            }
            else {
                const s = this.size - this.max;
                const entries = [];
                for (const key of keys.splice(0, s)) {
                    entries.push([key, this.data.get(key)]);
                    this.data.delete(key);
                    this.expirationMap.delete(key);
                }
                for (const [key, val] of entries) {
                    this.dispose(val, key, 'evict');
                }
                return;
            }
        }
    }
    get size() {
        return this.data.size;
    }
    purgeStale() {
        const n = Math.ceil(now());
        for (const exp in this.expirations) {
            if (exp === 'Infinity' || Number(exp) > n) {
                return;
            }
            /* c8 ignore start
             * mysterious need for a guard here?
             * https://github.com/isaacs/ttlcache/issues/26 */
            const keys = [...(this.expirations[exp] || [])];
            /* c8 ignore stop */
            const entries = [];
            delete this.expirations[exp];
            for (const key of keys) {
                entries.push([key, this.data.get(key)]);
                this.data.delete(key);
                this.expirationMap.delete(key);
            }
            for (const [key, val] of entries) {
                this.dispose(val, key, 'stale');
            }
        }
        if (this.size === 0) {
            this.cancelTimer();
        }
    }
    *entries() {
        for (const exp in this.expirations) {
            for (const key of this.expirations[exp]) {
                yield [key, this.data.get(key)];
            }
        }
        for (const key of this.immortalKeys) {
            yield [key, this.data.get(key)];
        }
    }
    *keys() {
        for (const exp in this.expirations) {
            for (const key of this.expirations[exp]) {
                yield key;
            }
        }
        for (const key of this.immortalKeys) {
            yield key;
        }
    }
    *values() {
        for (const exp in this.expirations) {
            for (const key of this.expirations[exp]) {
                yield this.data.get(key);
            }
        }
        for (const key of this.immortalKeys) {
            yield this.data.get(key);
        }
    }
    [Symbol.iterator]() {
        return this.entries();
    }
}
exports.TTLCache = TTLCache;
//# sourceMappingURL=index.js.map
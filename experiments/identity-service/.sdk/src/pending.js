/** A development store. Use a shared atomic store in a multi-process service. */
export class MemoryPendingLoginStore {
    logins = new Map();
    async save(login) {
        if (this.logins.has(login.state))
            throw new Error("pending login state already exists");
        this.logins.set(login.state, structuredClone(login));
    }
    async take(state) {
        const login = this.logins.get(state);
        this.logins.delete(state);
        return login === undefined ? undefined : structuredClone(login);
    }
}

// The console token lives in this module's memory for the tab only: never in localStorage, sessionStorage or a cookie.
// A reload asks for it again, on purpose (ADR 0007, rule 4).

let token = $state<string | null>(null);

export const auth = {
    get token(): string | null {
        return token;
    },
    get signedIn(): boolean {
        return token !== null;
    },
    signIn(t: string): void {
        token = t.trim() || null;
    },
    signOut(): void {
        token = null;
    },
};

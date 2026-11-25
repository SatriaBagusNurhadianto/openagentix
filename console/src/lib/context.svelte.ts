// What every page needs once signed in: the session (who, what may be done, demo or not) and the platform summary.
import { api, ApiError } from './api';
import type { Platform, Session } from './types';

let session = $state<Session | null>(null);
let platform = $state<Platform | null>(null);
let loading = $state(false);
let error = $state<string | null>(null);
let platformError = $state<string | null>(null);
// The sign-in form is replaced by the app while the token is being checked, so a refusal is kept here for the next form.
let signInError = $state('');

export const ctx = {
    get session() {
        return session;
    },
    get platform() {
        return platform;
    },
    get loading() {
        return loading;
    },
    get error() {
        return error;
    },
    get platformError() {
        return platformError;
    },
    get signInError() {
        return signInError;
    },
    setSignInError(m: string) {
        signInError = m;
    },
    async load(): Promise<boolean> {
        loading = true;
        error = null;
        platformError = null;
        try {
            session = await api.session();
        } catch (e) {
            session = null;
            error = e instanceof ApiError ? e.message : 'Could not reach the console API.';
            loading = false;
            return false;
        }
        try {
            platform = await api.platform();
        } catch (e) {
            platform = null;
            platformError = e instanceof ApiError ? e.message : 'Could not read the platform catalog and policy.';
        }
        loading = false;
        return true;
    },
    reset(): void {
        session = null;
        platform = null;
        error = null;
        platformError = null;
        loading = false;
    },
};

import type { Health } from '../types';

const BASE = '/api/v1';

/** Raised for any non-2xx response, or with status 0 when the network failed. */
export class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
        super(message);
        this.name = 'ApiError';
        this.status = status;
    }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
    let res: Response;
    try {
        res = await fetch(`${BASE}${path}`, { credentials: 'same-origin', ...init });
    } catch {
        throw new ApiError(0, 'network');
    }
    if (!res.ok) {
        let message = `HTTP ${res.status}`;
        try {
            const body = await res.json();
            if (body && typeof body.error === 'string') message = body.error;
        } catch {
            // The body is not JSON; keep the status text.
        }
        throw new ApiError(res.status, message);
    }
    if (res.status === 204) {
        return undefined as T;
    }
    return (await res.json()) as T;
}

export function getHealth(): Promise<Health> {
    return request<Health>('/health');
}

// Shapes of the JSON exchanged with the server (see app/workers/api_handlers.go).

/** GET /api/v1/health */
export interface Health {
    status: string;
    version: string;
}

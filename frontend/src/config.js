// Get the current hostname (works for both localhost and remote)
const hostname = window.location.hostname;

// Use the current host for API and WebSocket connections
export const API_BASE_URL = `http://${hostname}:8064`;
export const WS_BASE_URL = `ws://${hostname}:8064/ws`;

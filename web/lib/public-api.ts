// The API as the user's machine reaches it (the web server itself uses the in-network URL).
export const PUBLIC_API_URL = (process.env.TERRARIUM_PUBLIC_API_URL ?? "http://127.0.0.1:8080").replace(/\/$/, "");

// Runs once at server start: refuse to serve without a cookie secret in production.
export async function register() {
  if (
    process.env.NEXT_RUNTIME === "nodejs" &&
    process.env.NODE_ENV === "production" &&
    !process.env.TERRARIUM_WEB_SECRET
  ) {
    console.error("TERRARIUM_WEB_SECRET must be set in production");
    process.exit(1);
  }
}

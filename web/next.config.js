/** @type {import('next').NextConfig} */
// Static export, so the whole frontend can be embedded in the Go binary with
// go:embed. The export lands in out/, which postbuild copies into
// internal/webui/out where the embed directive expects it.
const isProd = process.env.NODE_ENV === 'production';

const nextConfig = {
  output: isProd ? 'export' : undefined,
  images: { unoptimized: true },
  trailingSlash: false,
  // In `next dev` the Go server runs separately on 7000, so the API is proxied
  // to it and the frontend behaves exactly as it does once embedded. The key is
  // omitted entirely in production, where a static export cannot rewrite.
  ...(isProd
    ? {}
    : {
        async rewrites() {
          return [
            { source: '/api/:path*', destination: 'http://localhost:7000/api/:path*' },
            { source: '/openapi.json', destination: 'http://localhost:7000/openapi.json' },
            { source: '/healthz', destination: 'http://localhost:7000/healthz' },
          ];
        },
      }),
};
module.exports = nextConfig;

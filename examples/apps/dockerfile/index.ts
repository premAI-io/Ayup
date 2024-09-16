console.log("Hello via Bun!");

Bun.serve({
  fetch(req, server) {
    const ip = server.requestIP(req);
    return new Response(`Your IP is ${ip?.address}`);
  },
});

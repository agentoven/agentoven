import { createServer, type Server } from 'node:http';

export interface Recorded {
  method: string;
  path: string;
  headers: Record<string, string | string[] | undefined>;
  body: any;
}

/** A real HTTP server on a loopback port that records requests and replies from a table. */
export class FakeControlPlane {
  requests: Recorded[] = [];
  private routes = new Map<string, { status: number; body: unknown }>();
  private server!: Server;
  url = '';

  reply(method: string, path: string, body: unknown, status = 200): void {
    this.routes.set(`${method} ${path}`, { status, body });
  }

  get last(): Recorded {
    return this.requests[this.requests.length - 1];
  }

  async start(): Promise<this> {
    this.server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const raw = Buffer.concat(chunks).toString();
        this.requests.push({
          method: req.method!,
          path: req.url!,
          headers: req.headers,
          body: raw ? JSON.parse(raw) : undefined,
        });
        const hit = this.routes.get(`${req.method} ${req.url}`) ?? { status: 404, body: { error: 'no route' } };
        const payload = JSON.stringify(hit.body);
        res.writeHead(hit.status, { 'Content-Type': 'application/json' });
        res.end(payload);
      });
    });
    await new Promise<void>((r) => this.server.listen(0, '127.0.0.1', r));
    this.url = `http://127.0.0.1:${(this.server.address() as { port: number }).port}`;
    return this;
  }

  async stop(): Promise<void> {
    await new Promise<void>((r) => this.server.close(() => r()));
  }
}

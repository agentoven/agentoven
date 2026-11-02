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
  private routes = new Map<string, { status: number; body: unknown; headers: Record<string, string> }>();
  private rawRoutes = new Map<string, { status: number; text: string; headers: Record<string, string> }>();
  private server!: Server;
  url = '';

  reply(method: string, path: string, body: unknown, status = 200, headers: Record<string, string> = {}): void {
    this.routes.set(`${method} ${path}`, { status, body, headers });
  }

  /** A reply that is not one JSON document (an NDJSON export), with the headers it carries. */
  replyRaw(method: string, path: string, text: string, headers: Record<string, string> = {}, status = 200): void {
    this.rawRoutes.set(`${method} ${path}`, { status, text, headers });
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
        const raw_ = this.rawRoutes.get(`${req.method} ${req.url}`);
        if (raw_) {
          res.writeHead(raw_.status, { 'Content-Type': 'application/x-ndjson', ...raw_.headers });
          res.end(raw_.text);
          return;
        }
        const hit = this.routes.get(`${req.method} ${req.url}`) ?? { status: 404, body: { error: 'no route' }, headers: {} };
        const payload = JSON.stringify(hit.body);
        res.writeHead(hit.status, { 'Content-Type': 'application/json', ...hit.headers });
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

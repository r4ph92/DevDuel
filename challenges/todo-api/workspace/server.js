'use strict';

const http = require('node:http');
const todos = require('./todos');

const port = Number(process.env.PORT ?? 3000);

// readJson returns the parsed body, or undefined when it is not JSON. An
// empty body is an empty object, which keeps the handlers free of null checks.
function readJson(req) {
	return new Promise((resolve) => {
		let raw = '';
		req.on('data', (chunk) => {
			raw += chunk;
		});
		req.on('end', () => {
			if (raw.trim() === '') {
				resolve({});
				return;
			}
			try {
				const parsed = JSON.parse(raw);
				resolve(parsed !== null && typeof parsed === 'object' ? parsed : undefined);
			} catch {
				resolve(undefined);
			}
		});
	});
}

function send(res, status, body) {
	if (body === null) {
		res.writeHead(status);
		res.end();
		return;
	}
	const payload = JSON.stringify(body);
	res.writeHead(status, { 'content-type': 'application/json' });
	res.end(payload);
}

// route matches a request to a handler. Ids are whole numbers; anything else
// is simply not a todo that exists.
function route(method, url, body) {
	const path = url.pathname.replace(/\/+$/, '') || '/';

	if (method === 'GET' && path === '/health') {
		return { status: 200, body: { status: 'ok' } };
	}
	if (path === '/todos') {
		if (method === 'GET') {
			return todos.listTodos(url.searchParams);
		}
		if (method === 'POST') {
			return todos.createTodo(body);
		}
		return { status: 405, body: { error: 'method_not_allowed' } };
	}

	const match = /^\/todos\/([^/]+)$/.exec(path);
	if (match === null) {
		return { status: 404, body: { error: 'not_found' } };
	}

	const id = Number(match[1]);
	if (!Number.isInteger(id)) {
		return todos.notFound();
	}

	switch (method) {
		case 'GET':
			return todos.getTodo(id);
		case 'PATCH':
			return todos.updateTodo(id, body);
		case 'DELETE':
			return todos.deleteTodo(id);
		default:
			return { status: 405, body: { error: 'method_not_allowed' } };
	}
}

const server = http.createServer((req, res) => {
	const handle = async () => {
		let body = {};
		if (req.method === 'POST' || req.method === 'PATCH') {
			body = await readJson(req);
			if (body === undefined) {
				send(res, 400, { error: 'invalid_json' });
				return;
			}
		}

		const result = route(req.method, new URL(req.url, 'http://todo-api'), body);
		send(res, result.status, result.body);
	};

	handle().catch((err) => {
		send(res, 500, { error: 'internal_error', message: String(err && err.message) });
	});
});

server.listen(port, '0.0.0.0', () => {
	console.log(`todo-api listening on ${port}`);
});

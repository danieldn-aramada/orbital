// GraphiQL page entry, bundled by `make vendor-graphiql` into web/shared/static/graphiql-<version>/.
import React from 'react';
import { createRoot } from 'react-dom/client';
import { GraphiQL } from 'graphiql';
import { createTransport } from '@graphiql/toolkit';
import 'graphiql/style.css';

const workers = { json: 'json.worker.js', graphql: 'graphql.worker.js' };

globalThis.MonacoEnvironment = {
  getWorker(_workerId, label) {
    return new Worker(new URL(workers[label] ?? 'editor.worker.js', import.meta.url));
  },
};

const transport = createTransport({ url: window.location.pathname });

createRoot(document.getElementById('graphiql')).render(
  React.createElement(GraphiQL, { transport, defaultEditorToolsVisibility: true }),
);

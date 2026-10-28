import { AcmeClient } from '@acme/sdk-v1';
import { InternalHttp } from './http';

const client = new AcmeClient("tok_123");
const internal = new InternalHttp();

client.request('/v1/accounts', { retries: 3 });
client.request('/v1/ping');
internal.request('/internal/ping');

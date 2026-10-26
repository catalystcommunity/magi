// Receives synthetic fixture credentials through stdin, never command arguments.
import { ApprovalService } from './approval.mjs';
let input = ''; for await (const chunk of process.stdin) input += chunk;
try {
  const p = JSON.parse(input);
  const service = new ApprovalService({...p.config, fault: point => {if (point === p.crash) process.kill(process.pid, 'SIGKILL');}});
  const result = service.approve(p.auth, p.review, Buffer.from(p.digest, 'hex'), p.command);
  if (p.crash === 'after-commit') process.kill(process.pid, 'SIGKILL');
  service.close(); process.stdout.write(result.approvalId);
} catch (e) {process.stderr.write(e.message); process.exitCode = 1;}

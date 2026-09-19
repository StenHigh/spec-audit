<?php

declare(strict_types=1);

namespace Demo;

use Demo\Support\Audited;

final class AuditedStore
{
    use Audited;

    public function __construct(private FileStore $log)
    {
    }

    public function record(string $event): void
    {
        $this->audit($this->log, $event);
    }
}

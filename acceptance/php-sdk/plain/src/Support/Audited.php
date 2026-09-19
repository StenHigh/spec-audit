<?php

declare(strict_types=1);

namespace Demo\Support;

use Demo\FileStore;

trait Audited
{
    public function audit(FileStore $log, string $event): void
    {
        $log->save('audit', $event);
    }
}

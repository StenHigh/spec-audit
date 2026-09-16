<?php

declare(strict_types=1);

namespace Demo\Contracts;

interface Notifier
{
    public function notify(string $message): void;
}

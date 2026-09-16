<?php

declare(strict_types=1);

namespace Demo;

final class FileStore
{
    /** @var array<string, string> */
    private array $items = [];

    public function save(string $key, string $value): void
    {
        $this->items[$key] = $value;
    }
}

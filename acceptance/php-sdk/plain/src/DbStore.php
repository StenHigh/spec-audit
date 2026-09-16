<?php

declare(strict_types=1);

namespace Demo;

final class DbStore
{
    /** @var array<string, string> */
    private array $rows = [];

    public function save(string $key, string $value): void
    {
        $this->rows[$key] = $value;
    }
}

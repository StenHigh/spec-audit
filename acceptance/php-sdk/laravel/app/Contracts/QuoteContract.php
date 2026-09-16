<?php

declare(strict_types=1);

namespace App\Contracts;

interface QuoteContract
{
    public function quote(int $amount): int;
}
